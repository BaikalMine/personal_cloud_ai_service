package gateway

import "testing"

func TestNormalizeQwen21ReferenceMentions(t *testing.T) {
	tests := []struct {
		input string
		count int
		want  string
	}{
		{"Recolor Picture 1 using Picture 2.", 2, "Recolor <image1> using <image2>."},
		{"Keep <image1> and image 2; ignore picture 3.", 2, "Keep <image1> and <image2>; ignore picture 3."},
		{"Use Photo #10 beside image1.", 10, "Use <image10> beside <image1>."},
		{"Do not change the picture frame.", 1, "Do not change the picture frame."},
	}
	for _, test := range tests {
		if got := normalizeQwen21ReferenceMentions(test.input, test.count); got != test.want {
			t.Errorf("normalizeQwen21ReferenceMentions(%q, %d) = %q; want %q", test.input, test.count, got, test.want)
		}
	}
}
