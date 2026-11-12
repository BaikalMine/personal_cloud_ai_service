package inference

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
)

func testJournal(t *testing.T) *outcomeJournal {
	t.Helper()
	cipher, err := contentcrypto.NewCipher("outcome-journal-test-secret-never-production")
	if err != nil {
		t.Fatal(err)
	}
	j, err := openOutcomeJournal(t.TempDir(), cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.root.Close() })
	return j
}

func testOutcome(t *testing.T, j *outcomeJournal) observedOutcome {
	t.Helper()
	response, err := j.cipher.EncryptBytesWithAAD(testResponse, responseAAD("test-job"))
	if err != nil {
		t.Fatal(err)
	}
	return observedOutcome{Version: 1, ID: "test-job", RunToken: "executor-token", LeaseToken: "gpu-permit", RequestHash: strings.Repeat("a", 64), Code: "executor_terminal", Status: 200, Response: response, ObservedAt: time.Now().UTC()}
}

func TestOutcomeJournalRoundTripAndBounds(t *testing.T) {
	j := testJournal(t)
	o := testOutcome(t, j)
	if err := j.save(o); err != nil {
		t.Fatal(err)
	}
	payload, err := j.root.ReadFile(outcomeName(o.ID))
	if err != nil || bytes.Contains(payload, []byte("executor-token")) || bytes.Contains(payload, []byte("green vase")) || bytes.Contains(payload, []byte("test-job")) {
		t.Fatalf("plaintext receipt: %v", err)
	}
	restored, err := j.read(outcomeName(o.ID))
	if err != nil || restored.ID != o.ID || !restored.ObservedAt.Equal(o.ObservedAt) || !bytes.Equal(restored.Response, o.Response) {
		t.Fatalf("receipt changed: %+v %v", restored, err)
	}
	// Repeated writes are atomic replacements, not concatenated JSON records.
	if err := j.save(o); err != nil {
		t.Fatal(err)
	}
	entries, _ := fs.ReadDir(j.root.FS(), ".")
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
	if err := j.remove(o.ID); err != nil {
		t.Fatal(err)
	}
	if err := j.remove(o.ID); err != nil {
		t.Fatal("idempotent removal", err)
	}
	for _, change := range []func(*observedOutcome){
		func(o *observedOutcome) { o.Code = "" }, func(o *observedOutcome) { o.Status = 504 },
		func(o *observedOutcome) { o.Response = nil }, func(o *observedOutcome) { o.Version = 2 },
		func(o *observedOutcome) { o.ObservedAt = time.Now().Add(time.Hour) }, func(o *observedOutcome) { o.RequestHash = "bad" },
		func(o *observedOutcome) { o.Response = make([]byte, maxOutcomeFileBytes) },
	} {
		invalid := o
		change(&invalid)
		if err := j.save(invalid); err == nil {
			t.Fatal("invalid receipt accepted")
		}
	}
}

func TestOutcomeJournalTamperingAndWrongKey(t *testing.T) {
	j := testJournal(t)
	o := testOutcome(t, j)
	if err := j.save(o); err != nil {
		t.Fatal(err)
	}
	name := outcomeName(o.ID)
	payload, _ := j.root.ReadFile(name)
	if err := j.write(outcomeName("another-job"), payload); err != nil {
		t.Fatal(err)
	}
	if _, err := j.read(outcomeName("another-job")); err == nil {
		t.Fatal("receipt moved to another ID accepted")
	}
	other, _ := contentcrypto.NewCipher("another-outcome-journal-secret-test-only")
	wrong := &outcomeJournal{root: j.root, cipher: other}
	if _, err := wrong.read(name); err == nil {
		t.Fatal("wrong key accepted")
	}
	payload[len(payload)-1] ^= 1
	if err := j.write(name, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := j.read(name); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestOutcomeJournalNeverFollowsReceiptSymlinks(t *testing.T) {
	j := testJournal(t)
	o := testOutcome(t, j)
	out := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(out, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := j.root.Symlink(out, outcomeName(o.ID)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := j.read(outcomeName(o.ID)); err == nil {
		t.Fatal("symlink receipt read")
	}
}

func TestOutcomeJournalRequiresPersistentAbsoluteDirectory(t *testing.T) {
	cipher, _ := contentcrypto.NewCipher("outcome-journal-test-secret-never-production")
	if _, err := openOutcomeJournal("relative", cipher); err == nil {
		t.Fatal("relative journal directory accepted")
	}
	path := filepath.Join(t.TempDir(), "file")
	os.WriteFile(path, []byte("not a directory"), 0600)
	if _, err := openOutcomeJournal(path, cipher); err == nil {
		t.Fatal("file journal root accepted")
	}
	j := testJournal(t)
	j.root.Close()
	if err := j.checkWritable(); err == nil {
		t.Fatal("closed journal reported writable")
	}
	w := &Worker{journal: j}
	if err := w.restoreOutcomes(context.Background()); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatal("closed journal ignored", err)
	}
}
