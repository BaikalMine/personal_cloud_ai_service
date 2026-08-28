package gateway

import (
	"context"
	"log"
	"strings"
)

// sensitiveContentTerms deliberately stay narrow: the flag is a privacy
// curtain for clearly adult prompts, not a judgement about ordinary content.
var sensitiveContentTerms = []string{
	"nsfw", "18+", "xxx", "porn", "pornographic", "erotic", "explicit sex",
	"nude", "nudity", "naked", "topless", "lingerie", "fetish", "intimate", "genitals",
	"nipples", "breasts", "penis", "vagina", "обнажен", "обнажён", "голая", "голый",
	"нагота", "эрот", "порно", "секс", "интим", "фетиш", "соски", "генитал",
}

func isSensitiveGeneration(input generationForm) bool {
	if strings.EqualFold(strings.TrimSpace(input.AssistantTemplate), "nsfw") {
		return true
	}
	return containsSensitiveContent(input.Positive, input.Negative, input.AssistantOriginal, input.AssistantSuggestion)
}

func containsSensitiveContent(values ...string) bool {
	for _, value := range values {
		lower := strings.ToLower(value)
		for _, term := range sensitiveContentTerms {
			if strings.Contains(lower, term) {
				return true
			}
		}
	}
	return false
}

func (a *App) classifyPendingSensitiveContent(ctx context.Context) {
	if a.store == nil || a.contentCipher == nil {
		return
	}
	items, err := a.store.ListUnclassifiedSensitiveContent(ctx, 500)
	if err != nil {
		log.Printf("list content awaiting sensitivity classification: %v", err)
		return
	}
	for _, item := range items {
		prompt, promptErr := a.contentCipher.Decrypt(item.PromptCipher)
		response, responseErr := a.contentCipher.Decrypt(item.ResponseCipher)
		metadata, metadataErr := a.contentCipher.Decrypt(item.MetadataCipher)
		if promptErr != nil || responseErr != nil || metadataErr != nil {
			log.Printf("decrypt content %d for sensitivity classification", item.ID)
			continue
		}
		if err := a.store.SetContentEventSensitive(ctx, item.ID, containsSensitiveContent(prompt, response, metadata)); err != nil {
			log.Printf("store sensitivity classification for content %d: %v", item.ID, err)
		}
	}
}

