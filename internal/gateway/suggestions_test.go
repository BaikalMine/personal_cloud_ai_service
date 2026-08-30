package gateway

import "testing"

func TestParseSuggestionLinksRejectsUnsafeTargets(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
		bad   bool
	}{
		{name: "public links", input: "https://huggingface.co/model\nhttps://github.com/org/project", want: 2},
		{name: "deduplicates", input: "https://example.com\nhttps://example.com", want: 1},
		{name: "loopback", input: "http://127.0.0.1/model", bad: true},
		{name: "private lan", input: "http://192.168.1.10/file", bad: true},
		{name: "credentials", input: "https://user:pass@example.com/file", bad: true},
		{name: "too many", input: "https://a.example\nhttps://b.example\nhttps://c.example\nhttps://d.example", bad: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			links, err := parseSuggestionLinks(test.input)
			if (err != nil) != test.bad {
				t.Fatalf("parseSuggestionLinks() error = %v, want bad=%v", err, test.bad)
			}
			if !test.bad && len(links) != test.want {
				t.Fatalf("links = %#v, want %d", links, test.want)
			}
		})
	}
}

func TestVirusTotalStatus(t *testing.T) {
	if got := virusTotalStatus("in_progress"); got != "in-progress" {
		t.Fatalf("in_progress = %q", got)
	}
	if got := virusTotalStatus("other"); got != "queued" {
		t.Fatalf("other = %q", got)
	}
}
