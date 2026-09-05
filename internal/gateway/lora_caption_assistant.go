package gateway

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"
)

const maxLoraCaptionRequestBytes = maxLoraTrainingImageBytes + (512 << 10)

var (
	errLoraCaptionCSRF     = errors.New("проверка безопасности не пройдена")
	errLoraCaptionTooLarge = errors.New("изображение должно быть не больше 24 МБ")
)

type loraCaptionSubmission struct {
	TriggerWord string
	ConceptType string
	Filename    string
	MIMEType    string
	Image       []byte
}

func (a *App) readLoraCaptionSubmission(w http.ResponseWriter, r *http.Request) (loraCaptionSubmission, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoraCaptionRequestBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		return loraCaptionSubmission{}, errors.New("не удалось прочитать запрос автописания")
	}
	result := loraCaptionSubmission{}
	csrfVerified := false
	imageCount := 0
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			if strings.Contains(strings.ToLower(partErr.Error()), "too large") {
				return result, errLoraCaptionTooLarge
			}
			return result, errors.New("не удалось прочитать запрос автописания")
		}
		name := part.FormName()
		if part.FileName() == "" {
			value, readErr := io.ReadAll(io.LimitReader(part, (16<<10)+1))
			part.Close()
			if readErr != nil || len(value) > 16<<10 {
				return result, errors.New("поле запроса автописания слишком длинное")
			}
			switch name {
			case "csrf":
				csrfVerified = a.validCSRFValue(r, strings.TrimSpace(string(value)))
				if !csrfVerified {
					return result, errLoraCaptionCSRF
				}
			case "trigger_word":
				result.TriggerWord = strings.TrimSpace(string(value))
			case "concept_type":
				result.ConceptType = strings.TrimSpace(string(value))
			}
			continue
		}
		if name != "image" || !csrfVerified {
			part.Close()
			if !csrfVerified {
				return result, errLoraCaptionCSRF
			}
			return result, errors.New("за один запрос можно отправить только одно изображение")
		}
		imageCount++
		if imageCount > 1 {
			part.Close()
			return result, errors.New("за один запрос можно отправить только одно изображение")
		}
		payload, readErr := io.ReadAll(io.LimitReader(part, maxLoraTrainingImageBytes+1))
		filename := filepath.Base(part.FileName())
		part.Close()
		if readErr != nil || len(payload) == 0 || len(payload) > maxLoraTrainingImageBytes {
			clear(payload)
			return result, errLoraCaptionTooLarge
		}
		result.Image = payload
		result.Filename = filename
	}
	if !csrfVerified {
		return result, errLoraCaptionCSRF
	}
	if imageCount != 1 || len(result.Image) == 0 {
		return result, errors.New("добавьте одно изображение для описания")
	}
	headerLength := min(len(result.Image), 512)
	mimeType := http.DetectContentType(result.Image[:headerLength])
	switch mimeType {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return result, fmt.Errorf("%s: поддерживаются только PNG, JPG и WebP", result.Filename)
	}
	prepared, preparedMIME, _, err := prepareVisionReference(result.Image, mimeType)
	if err != nil {
		return result, fmt.Errorf("%s: не удалось подготовить изображение", result.Filename)
	}
	if &prepared[0] != &result.Image[0] {
		clear(result.Image)
	}
	result.Image = prepared
	result.MIMEType = preparedMIME
	return result, nil
}

func ensureLoraCaptionTrigger(trigger, caption string) string {
	trigger = strings.TrimSpace(trigger)
	caption = strings.TrimSpace(caption)
	if trigger == "" {
		return caption
	}
	for {
		rest, matched := trimLoraTriggerPrefix(caption, trigger)
		if !matched {
			break
		}
		caption = rest
	}
	if caption == "" {
		return trigger
	}
	return trigger + ", " + caption
}

func trimLoraTriggerPrefix(caption, trigger string) (string, bool) {
	captionRunes := []rune(strings.TrimSpace(caption))
	triggerRunes := []rune(strings.TrimSpace(trigger))
	if len(triggerRunes) == 0 || len(captionRunes) < len(triggerRunes) || !strings.EqualFold(string(captionRunes[:len(triggerRunes)]), string(triggerRunes)) {
		return caption, false
	}
	if len(captionRunes) > len(triggerRunes) {
		next := captionRunes[len(triggerRunes)]
		if !unicode.IsSpace(next) && !unicode.IsPunct(next) {
			return caption, false
		}
	}
	rest := strings.TrimLeftFunc(string(captionRunes[len(triggerRunes):]), func(value rune) bool {
		return unicode.IsSpace(value) || unicode.IsPunct(value)
	})
	return rest, true
}
