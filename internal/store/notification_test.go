package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateNotificationTextBoundsCharacters(t *testing.T) {
	value := strings.Repeat("ошибка ", 100)
	result := truncateNotificationText(value, 500)
	if !utf8.ValidString(result) {
		t.Fatal("truncated notification is not valid UTF-8")
	}
	if count := len([]rune(result)); count != 500 {
		t.Fatalf("truncated notification length=%d, want 500", count)
	}
	if !strings.HasSuffix(result, "…") {
		t.Fatalf("truncated notification does not end with an ellipsis: %q", result)
	}
}

func TestTruncateNotificationTextKeepsShortMessage(t *testing.T) {
	if result := truncateNotificationText("  Готово  ", 500); result != "Готово" {
		t.Fatalf("short notification=%q", result)
	}
}
