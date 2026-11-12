package componentinspection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testRequest(name string) Request {
	return Request{Version: 1, RequestID: "component-request-0001", References: []Reference{{Kind: "lora", Name: name}}}
}

func waitInspection(t *testing.T, inspector *Inspector, request Request) Response {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := inspector.Inspect(request)
		if err != nil {
			t.Fatal(err)
		}
		if !response.ValidFor(request) {
			t.Fatalf("invalid own response: %+v", response)
		}
		if response.State == "complete" {
			return response
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("inspection did not finish")
	return Response{}
}

func TestComponentNamesAndProtocol(t *testing.T) {
	for _, name := range []string{"../a.pth", "/a.pth", `C:\a.pth`, `a.pth:secret`, `a//b.pth`, `a/./b.pth`, `a/../b.pth`, `a./b.pth`, ` a.pth`, `a.pth `, "a\n.pth", "config.json", "file"} {
		if _, ok := NormalizeName(name); ok {
			t.Errorf("accepted unsafe/unsupported name %q", name)
		}
	}
	if name, ok := NormalizeName(`Trained\person\v1.safetensors`); !ok || name != "Trained/person/v1.safetensors" {
		t.Fatal(name, ok)
	}
	r := testRequest("model.pth")
	if !r.Valid() {
		t.Fatal("valid request rejected")
	}
	r.References = append(r.References, r.References[0])
	if r.Valid() {
		t.Fatal("duplicate references accepted")
	}
	for _, roots := range []map[string][]string{{"other": {t.TempDir()}}, {"lora": {"relative"}}, {"lora": {}}} {
		if i, err := New(roots); err == nil {
			i.Close()
			t.Fatal("invalid roots accepted")
		}
	}
}

func TestInspectorActualBytesCacheReplacementAndRestart(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "adapter.safetensors")
	if err := os.WriteFile(name, []byte("original bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	i, err := New(map[string][]string{"lora": {dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	r := testRequest("adapter.safetensors")
	first, err := i.Inspect(r)
	if err != nil || first.State != "pending" {
		t.Fatal("hash did not run asynchronously", first, err)
	}
	ready := waitInspection(t, i, r)
	want := sha256.Sum256([]byte("original bytes"))
	if ready.Items[0].SHA256 != hex.EncodeToString(want[:]) || ready.Items[0].Bytes != 14 {
		t.Fatal("wrong digest", ready)
	}
	cached := waitInspection(t, i, r)
	if !cached.Items[0].Cached || cached.Items[0].HashedAt != ready.Items[0].HashedAt {
		t.Fatal("stable file was rehashed", cached)
	}
	oldInfo, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(name, name+".old"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, []byte("modified bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(name, oldInfo.ModTime(), oldInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	changed := waitInspection(t, i, r)
	if changed.Items[0].SHA256 == ready.Items[0].SHA256 {
		t.Fatal("same size/mtime replacement reused cached hash")
	}
	if err = os.WriteFile(name, []byte("in-place edit!"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(name, time.Now(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	edited := waitInspection(t, i, r)
	if edited.Items[0].SHA256 == changed.Items[0].SHA256 {
		t.Fatal("in-place edit reused cached hash")
	}
	i.Close()
	if _, err = i.Inspect(r); !errors.Is(err, context.Canceled) {
		t.Fatal("closed inspector accepted work", err)
	}
	restarted, err := New(map[string][]string{"lora": {dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if got, err := restarted.Inspect(r); err != nil || got.State != "pending" {
		t.Fatal("restart trusted an old stat cache", got, err)
	}
	if got := waitInspection(t, restarted, r); got.Items[0].SHA256 != edited.Items[0].SHA256 {
		t.Fatal("restart changed file identity", got)
	}
	if err = os.Remove(name); err != nil {
		t.Fatal(err)
	}
	missing := waitInspection(t, restarted, r)
	if missing.Items[0].State != "missing" || missing.Items[0].SHA256 != "" {
		t.Fatal("deleted file retained digest", missing)
	}
}

func TestInspectorRootsAmbiguityAndLinks(t *testing.T) {
	one, two, outside := t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{one, two, outside} {
		if err := os.WriteFile(filepath.Join(dir, "model.pth"), []byte("private bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	i, err := New(map[string][]string{"lora": {one, two}})
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	if got := waitInspection(t, i, testRequest("model.pth")); got.Items[0].State != "ambiguous" {
		t.Fatal("ambiguous file selected", got)
	}
	r := testRequest("model.pth")
	r.References[0].Kind = "vae"
	if got := waitInspection(t, i, r); got.Items[0].State != "unconfigured" {
		t.Fatal(got)
	}
	if err = os.Symlink(filepath.Join(outside, "model.pth"), filepath.Join(one, "escape.pth")); err != nil {
		t.Skip("symlinks unavailable", err)
	}
	if got := waitInspection(t, i, testRequest("escape.pth")); got.Items[0].State != "unavailable" || got.Items[0].SHA256 != "" {
		t.Fatal("escaped configured root", got)
	}
	if err = os.Mkdir(filepath.Join(one, "directory.pth"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := waitInspection(t, i, testRequest("directory.pth")); got.Items[0].State != "unavailable" {
		t.Fatal("nonregular file accepted", got)
	}
}

type mutatingReader struct {
	reader io.Reader
	mutate func()
	once   sync.Once
}

func (reader *mutatingReader) Read(data []byte) (int, error) {
	reader.once.Do(reader.mutate)
	return reader.reader.Read(data)
}

func TestInspectorRejectsChangesDuringRead(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "overwrite", true: "replace"}[replace], func(t *testing.T) {
			dir := t.TempDir()
			filename := filepath.Join(dir, "model.pth")
			if err := os.WriteFile(filename, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			i, err := New(map[string][]string{"lora": {dir}})
			if err != nil {
				t.Fatal(err)
			}
			defer i.Close()
			ref := testRequest("model.pth").References[0]
			file, state := i.locate(ref)
			if state != "ready" {
				t.Fatal(state)
			}
			defer file.close()
			reader := &mutatingReader{reader: file.file, mutate: func() {
				if replace {
					if err := os.Rename(filename, filename+".old"); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filename, []byte("changed while hashing"), 0600); err != nil {
					t.Fatal(err)
				}
			}}
			got, _ := i.hashOpened(context.Background(), ref, file, reader)
			if got.State != "unstable" || got.SHA256 != "" {
				t.Fatal("unstable bytes labelled verified", got)
			}
		})
	}
}

func TestInspectorCancellationAndConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.pth"), []byte(strings.Repeat("data", 1024)), 0600); err != nil {
		t.Fatal(err)
	}
	i, err := New(map[string][]string{"lora": {dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, _ := i.hash(ctx, testRequest("model.pth").References[0]); result.SHA256 != "" {
		t.Fatal("cancelled hash accepted")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				if _, err := i.Inspect(testRequest("model.pth")); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if got := waitInspection(t, i, testRequest("model.pth")); got.Items[0].State != "verified" {
		t.Fatal(got)
	}
}
