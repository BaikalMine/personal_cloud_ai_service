package promptassistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const LongVideoContextTokens = 128 * 1024

func longVideoResponseSchema(parts int) map[string]any {
	schema := ltxResponseSchema(ModeTextToVideo)
	schema["required"] = []string{"references", "prompt", "overall_soundscape", "non_diegetic_music"}
	properties := schema["properties"].(map[string]any)
	delete(properties, "audio_prompt")
	properties["prompt"] = map[string]any{"type": "string", "minLength": 1, "maxLength": PromptCharacterLimitForVideo(ModeTextToVideo, parts) - 2100}
	properties["overall_soundscape"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 1536}
	properties["non_diegetic_music"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 512}
	return schema
}

func assembleLongVideoContent(raw string) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stripModelEnvelope(raw)), &fields); err != nil {
		return "", errors.New("локальная модель не вернула полный структурированный план длинного видео")
	}
	var visual, sound, music string
	if json.Unmarshal(fields["prompt"], &visual) != nil || json.Unmarshal(fields["overall_soundscape"], &sound) != nil || json.Unmarshal(fields["non_diegetic_music"], &music) != nil || strings.TrimSpace(sound) == "" || strings.TrimSpace(music) == "" {
		return "", errors.New("в плане длинного видео отсутствует звуковая сцена или указание музыки")
	}
	if utf8.RuneCountInString(sound) > 1536 || utf8.RuneCountInString(music) > 512 {
		return "", errors.New("звуковое описание длинного видео слишком большое")
	}
	// Some models repeat the sound fields inside prompt despite the schema.
	// Only the explicit structured fields supply the canonical final audio tail.
	if tail := longVideoSoundPattern.FindStringIndex(visual); tail != nil {
		visual = visual[:tail[0]]
	}
	visual = strings.TrimSpace(visual) + "\n\noverall_soundscape:\n" + strings.TrimSpace(sound) + "\n\nnon_diegetic_music:\n" + strings.TrimSpace(music)
	sections := longVideoSectionPattern.FindAllStringSubmatchIndex(visual, -1)
	expected := []string{"subject_definitions", "summary", "retention_analysis", "detailed_description", "overall_soundscape", "non_diegetic_music"}
	if len(sections) != len(expected) {
		return "", errors.New("локальная модель не вернула все шесть разделов плана длинного видео")
	}
	for index, section := range sections {
		end := len(visual)
		if index+1 < len(sections) {
			end = sections[index+1][0]
		}
		if !strings.EqualFold(visual[section[2]:section[3]], expected[index]) || strings.TrimSpace(visual[section[1]:end]) == "" {
			return "", errors.New("локальная модель вернула неполный или неверно упорядоченный план длинного видео")
		}
	}
	fields["prompt"], _ = json.Marshal(visual)
	encoded, err := json.Marshal(fields)
	return string(encoded), err
}

func ValidLongVideoParts(parts int) bool { return parts >= 2 && parts <= 20 }

func PromptCharacterLimitForVideo(mode Mode, parts int) int {
	if mode == ModeTextToVideo && ValidLongVideoParts(parts) {
		return 4000 + parts*1600
	}
	return PromptCharacterLimit(mode)
}

func (c *Client) PolicyForVideoRequest(mode Mode, profile Profile, video VideoContext, think, hasImages bool) RequestPolicy {
	policy := c.PolicyForRequest(mode, profile, think, hasImages)
	if mode != ModeTextToVideo || !IsMiniMaxH3Profile(profile) || !ValidLongVideoParts(video.LongParts) {
		return policy
	}
	policy.NumCtx = LongVideoContextTokens
	budget := 4096 + video.LongParts*768
	if think {
		budget += 8192
	}
	policy.NumPredict = max(policy.NumPredict, budget)
	// Include cold model loading, visual references and reasoning, not queue time.
	policy.Timeout = max(policy.Timeout, time.Duration(8+video.LongParts)*time.Minute)
	policy.TimeoutMS = policy.Timeout.Milliseconds()
	return policy
}

var longVideoShotPattern = regexp.MustCompile(`(?i)\[Shot\s+(\d+)\](?:[ \t]+At[ \t]+(\d{2}):(\d{2})\.(\d{3}))?[ \t]*,?`)
var longVideoSoundPattern = regexp.MustCompile(`(?im)\n\s*(?:overall_soundscape|non_diegetic_music)\s*:`)
var longVideoSectionPattern = regexp.MustCompile(`(?im)^[ \t]*(subject_definitions|summary|retention_analysis|detailed_description|overall_soundscape|non_diegetic_music)[ \t]*:`)

// Shared by assistant completion and generation preflight, so an accepted plan
// can be submitted without changing its section markers or timestamps.
func ValidateLongVideoTimeline(prompt string, parts int) error {
	if !ValidLongVideoParts(parts) {
		return errors.New("длинное видео MiniMax H3: выберите от 2 до 20 частей")
	}
	matches := longVideoShotPattern.FindAllStringSubmatchIndex(prompt, -1)
	if len(matches) != parts {
		return fmt.Errorf("план длинного видео: нужны ровно %d раздела [Shot N], по одному на 15 секунд; подготовьте план через промт-ассистент или исправьте промт", parts)
	}
	for index, match := range matches {
		number, _ := strconv.Atoi(prompt[match[2]:match[3]])
		millis := 0
		if match[4] >= 0 {
			minutes, _ := strconv.Atoi(prompt[match[4]:match[5]])
			seconds, _ := strconv.Atoi(prompt[match[6]:match[7]])
			fraction, _ := strconv.Atoi(prompt[match[8]:match[9]])
			if seconds >= 60 {
				return errors.New("план длинного видео: некорректная отметка времени")
			}
			millis = (minutes*60+seconds)*1000 + fraction
		} else if index > 0 {
			return fmt.Errorf("для [Shot %d] укажите At %02d:%02d.000", index+1, index*15/60, index*15%60)
		}
		if number != index+1 || millis != index*15000 {
			return fmt.Errorf("план длинного видео: ожидается [Shot %d] At %02d:%02d.000", index+1, index*15/60, index*15%60)
		}
		end := len(prompt)
		if index+1 < len(matches) {
			end = matches[index+1][0]
		}
		if sound := longVideoSoundPattern.FindStringIndex(prompt[match[1]:end]); sound != nil {
			end = match[1] + sound[0]
		}
		if strings.TrimSpace(prompt[match[1]:end]) == "" {
			return fmt.Errorf("опишите действие в части %d", index+1)
		}
	}
	return nil
}
