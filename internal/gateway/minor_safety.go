package gateway

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

var errMinorSexualContent = errors.New("нельзя создавать сексуализированный контент с участием несовершеннолетних или персонажей с неоднозначным возрастом")

var minorAgePattern = regexp.MustCompile(`(?:^|\s)(?:[0-9]|1[0-7])\s*(?:yo|y o|year old|years old|лет|год|года)(?:\s|$)`)

var minorPromptTerms = []string{
	"minor", "minors", "underage", "under age", "child", "children", "kid", "kids", "teen", "teenage",
	"preteen", "pre teen", "schoolgirl", "school boy", "schoolboy", "loli", "lolita", "shota",
	"infant", "toddler", "pubescent", "barely legal", "несовершеннолет", "малолет",
	"ребен", "подрост", "школьниц", "школьник", "девочк", "мальчик",
}

var sexualPromptTerms = []string{
	"nsfw", "xxx", "porn", "porno", "sexual", "sex", "erotic", "erotica", "explicit",
	"nude", "nudity", "naked", "topless", "lingerie", "fetish", "hentai", "bdsm",
	"breasts", "boobs", "nipples", "genitals", "penis", "vagina", "masturb", "orgasm",
	"cum", "penetration", "onlyfans", "slut", "обнажен", "голая", "голый", "нагота",
	"порно", "секс", "сексуал", "эрот", "откровенн", "фетиш", "соски", "генитал",
	"мастурб", "оргазм", "эякуля", "проникнов", "хентай", "бдсм",
}

// validateGenerationPrompt rejects the combination of sexualized content and
// indications that the person is underage or their age is deliberately unclear.
// It intentionally does not attempt to infer age from an uploaded photograph.
func validateGenerationPrompt(values ...string) error {
	for _, value := range values {
		normalized := normalizeSafetyPrompt(value)
		if normalized == "" {
			continue
		}
		if containsSafetyTerm(normalized, sexualPromptTerms) &&
			(containsSafetyTerm(normalized, minorPromptTerms) || minorAgePattern.MatchString(normalized)) {
			return errMinorSexualContent
		}
	}
	return nil
}

func normalizeSafetyPrompt(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	previousSpace := true
	for _, character := range strings.ToLower(value) {
		if character == 'ё' {
			character = 'е'
		}
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			result.WriteRune(character)
			previousSpace = false
			continue
		}
		if !previousSpace {
			result.WriteByte(' ')
			previousSpace = true
		}
	}
	return strings.TrimSpace(result.String())
}

func containsSafetyTerm(normalized string, terms []string) bool {
	tokens := strings.Fields(normalized)
	padded := " " + normalized + " "
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if strings.Contains(term, " ") {
			if strings.Contains(padded, " "+term+" ") {
				return true
			}
			continue
		}
		for _, token := range tokens {
			if token == term || safetyTermUsesPrefix(term) && strings.HasPrefix(token, term) {
				return true
			}
		}
	}
	return false
}

func safetyTermUsesPrefix(term string) bool {
	switch term {
	case "teen", "teenage", "preteen", "schoolgirl", "schoolboy", "loli", "lolita", "shota",
		"porn", "sexual", "erotic", "fetish", "masturb", "orgasm",
		"несовершеннолет", "малолет", "ребен", "подрост", "школьниц", "школьник", "девочк", "мальчик",
		"обнажен", "сексуал", "эрот", "откровенн", "генитал", "эякуля", "проникнов":
		return true
	default:
		return false
	}
}
