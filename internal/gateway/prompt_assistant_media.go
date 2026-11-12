package gateway

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/thumbnail"
)

// Resolve only active, user-owned uploads. Media bytes never enter client context
// fingerprints or logs; the existing encrypted durable request owns the snapshot.
func (a *App) prepareAssistantVideo(ctx context.Context, userID int64, r *http.Request, video *promptassistant.VideoContext) error {
	if r.Form.Get("generation_workflow") != aiHubWorkflowID {
		return nil
	}
	video.AIHubMode = r.Form.Get("aihub_mode")
	if video.AIHubMode == "" {
		video.AIHubMode = "video"
	}
	if !miniMaxH3Allowed(video.AIHubMode, "video", "photo", "carousel") {
		return errors.New("некорректный режим AIHUB")
	}
	video.AudioSource = r.Form.Get("aihub_audio_source")
	if video.AudioSource == "" {
		video.AudioSource = "native"
	}
	if !miniMaxH3Allowed(video.AudioSource, "native", "original", "ltx") {
		return errors.New("некорректный источник звука")
	}
	video.AudioReference = false // Seed-VC is not an H3 conditioning input.
	video.AIHubTask = r.Form.Get("aihub_task")
	if video.AIHubTask == "" {
		video.AIHubTask = "reference"
	}
	if !miniMaxH3Allowed(video.AIHubTask, "reference", "replace_character", "replace_clothing") || video.AIHubTask != "reference" && video.AIHubMode != "video" {
		return errors.New("некорректная задача AIHUB")
	}
	video.AudioReference = r.Form.Get("aihub_audio_reference") == "true"
	video.AIHubT8 = r.Form.Get("aihub_t8") == "true"
	if (video.AudioReference || video.AIHubT8) && (video.AIHubMode == "carousel" || video.AudioSource == "native") {
		return errors.New("выберите внешний источник звука для H3")
	}
	video.VideoReference = false
	if video.AIHubMode == "carousel" {
		video.AudioSource = "none"
	}
	if video.AIHubMode == "photo" && video.AudioSource == "original" {
		return errors.New("звук видео недоступен в режиме по фото")
	}
	if video.AudioSource == "ltx" {
		video.AudioPrompt = strings.TrimSpace(r.Form.Get("aihub_audio_prompt"))
		if len([]rune(video.AudioPrompt)) > 12000 {
			return errors.New("описание звука слишком длинное")
		}
	}
	if video.AIHubMode != "carousel" && r.Form.Get("aihub_voice_conversion") == "true" {
		if err := a.validateGenerationAudio(r.Form.Get("input_audio"), userID); err != nil {
			return err
		}
		video.VoiceReference = true
	}
	carouselV2 := video.AIHubMode == "carousel" && r.Form.Get("aihub_carousel_pipeline") == "v2"
	if video.AIHubMode == "photo" || carouselV2 && r.Form.Get("aihub_carousel_source") == "prompt" {
		return nil
	}
	filename := strings.TrimSpace(r.Form.Get("input_video"))
	if filename == "" {
		return errors.New("добавьте видеореференс для выбранного режима")
	}
	if err := a.validateGenerationVideo(filename, userID); err != nil {
		return err
	}
	start := 0.0
	if value := r.Form.Get("video_reference_start"); value != "" {
		var err error
		start, err = strconv.ParseFloat(value, 64)
		if err != nil {
			return errors.New("некорректное начало видео")
		}
	}
	duration := float64(video.DurationSeconds)
	if video.AIHubMode == "carousel" && !carouselV2 {
		frames, err := strconv.Atoi(r.Form.Get("aihub_photo_frames"))
		if err != nil || frames < 22 || frames > 124 {
			return errors.New("некорректная длина фотокарусели")
		}
		duration = float64(frames) / 24
	}
	matchDuration := r.Form.Get("aihub_match_duration") == "true"
	if matchDuration {
		start, duration = 0, 5
	}
	if start < 0 || start > 600 || duration > 15 {
		return errors.New("некорректный фрагмент видео")
	}
	if matchDuration && video.AIHubMode != "video" {
		return errors.New("длина видеореференса доступна только в режиме видео по видео")
	}
	var data []byte
	err := a.withInputVideoBudget(ctx, filename, 90*time.Second, func(ctx context.Context, local, mime string) error {
		if matchDuration {
			seconds, err := probeAIHubReferenceDuration(ctx, local, mime)
			if err != nil {
				return err
			}
			start, duration = 0, seconds
			video.DurationSeconds = int(math.Ceil(seconds))
		}
		pages, err := thumbnail.StoryboardSequence(ctx, local, mime, start, duration)
		if err != nil {
			return err
		}
		for _, page := range pages {
			video.VideoStoryboards = append(video.VideoStoryboards, page.Image)
			video.VideoStoryboardTimes = append(video.VideoStoryboardTimes, page.Times)
		}
		data = pages[0].Image
		return nil
	})
	if err != nil {
		return assistantVideoError(err)
	}
	video.VideoReference, video.VideoStoryboard, video.VideoStart, video.VideoSampleDuration = true, data, start, duration
	return nil
}

func (a *App) assistantVideoStoryboard(ctx context.Context, filename string, start, duration float64) ([]byte, error) {
	var data []byte
	err := a.withInputVideo(ctx, filename, func(ctx context.Context, local, mime string) error {
		var err error
		data, err = thumbnail.Storyboard(ctx, local, mime, start, duration)
		return err
	})
	return data, assistantVideoError(err)
}

func assistantVideoError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return errors.New("подготовка видеореференса отменена")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, thumbnail.ErrLimit):
		return errors.New("не удалось извлечь кадры видеореференса в пределах лимита обработки; попробуйте более короткий фрагмент или видео меньшего разрешения")
	case errors.Is(err, thumbnail.ErrUnsupported):
		return errors.New("формат или видеокодек референса не поддерживается; экспортируйте видео в MP4 с кодеком H.264 и загрузите снова")
	case errors.Is(err, thumbnail.ErrInvalid):
		return errors.New("не удалось прочитать кадры видеореференса; проверьте, что выбранный фрагмент есть в видео, или экспортируйте ролик в MP4 (H.264) и загрузите снова")
	default:
		return err
	}
}

// Callers must validate upload ownership before downloading from ComfyUI.
func (a *App) withInputVideo(ctx context.Context, filename string, use func(context.Context, string, string) error) error {
	return a.withInputVideoBudget(ctx, filename, 30*time.Second, use)
}

func (a *App) withInputVideoBudget(ctx context.Context, filename string, budget time.Duration, use func(context.Context, string, string) error) error {
	a.mediaProbeOnce.Do(func() { a.mediaProbeSlots = make(chan struct{}, 2) })
	select {
	case a.mediaProbeSlots <- struct{}{}:
		defer func() { <-a.mediaProbeSlots }()
	default:
		return errors.New("разбор видео занят; повторите через несколько секунд")
	}
	normalized, err := normalizeComfyDataPath(filename, false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/view")
	query := endpoint.Query()
	query.Set("filename", path.Base(normalized))
	query.Set("subfolder", path.Dir(normalized))
	query.Set("type", "input")
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), http.NoBody)
	if err != nil {
		return errors.New("не удалось открыть видеореференс")
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return errors.New("не удалось загрузить видеореференс для проверки")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > 512<<20 {
		return errors.New("видеореференс недоступен или превышает 512 МБ")
	}
	file, err := os.CreateTemp("", "gateway-input-video-*")
	if err != nil {
		return errors.New("не удалось подготовить файл видеореференса")
	}
	defer os.Remove(file.Name())
	defer file.Close()
	size, err := io.Copy(file, io.LimitReader(response.Body, (512<<20)+1))
	if err != nil || size == 0 || size > 512<<20 {
		return errors.New("видеореференс пуст, повреждён или превышает 512 МБ")
	}
	if err := file.Close(); err != nil {
		return errors.New("не удалось подготовить файл видеореференса")
	}
	mime := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	return use(ctx, file.Name(), mime)
}
