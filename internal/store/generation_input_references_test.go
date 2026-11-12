package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestCanonicalGenerationInputNames(t *testing.T) {
	for _, names := range [][]string{{""}, {"/file.png"}, {"../file.png"}, {"a/../file.png"}, {"a//file.png"}, {"a\\file.png"}, {"a:file.png"}, {"a/\x00.png"}, {"a/\n.png"}, {"."}, {".."}} {
		if _, err := canonicalGenerationInputNames(names); !errors.Is(err, ErrGenerationInputUnavailable) {
			t.Fatalf("accepted %q: %v", names, err)
		}
	}
	if got, err := canonicalGenerationInputNames(nil); err != nil || got != nil {
		t.Fatal(got, err)
	}
	if got, err := canonicalGenerationInputNames([]string{}); err != nil || got == nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if got, err := canonicalGenerationInputNames([]string{"owner/b.png", "owner/a.png", "owner/b.png"}); err != nil || !reflect.DeepEqual(got, []string{"owner/a.png", "owner/b.png"}) {
		t.Fatal(got, err)
	}
}
