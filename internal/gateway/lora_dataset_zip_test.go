package gateway

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
)

func datasetTestZIP(t *testing.T, names []string, data [][]byte) []byte {
	t.Helper()
	var body bytes.Buffer
	w := zip.NewWriter(&body)
	for i, name := range names {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func TestLoraDatasetZIPPathsAndLimits(t *testing.T) {
	for _, names := range [][]string{{"../photo.png"}, {"/photo.png"}, {"a/../photo.png"}, {`a\photo.png`}, {"C:photo.png"}, {"photo.png", "PHOTO.png"}} {
		t.Run(strings.Join(names, "-"), func(t *testing.T) {
			data := datasetTestZIP(t, names, make([][]byte, len(names)))
			r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loraDatasetZIPEntries(r); err == nil {
				t.Fatal("invalid ZIP paths accepted")
			}
		})
	}
	data := datasetTestZIP(t, []string{"images/", "images/photo.png"}, [][]byte{nil, []byte("image")})
	r, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	entries, err := loraDatasetZIPEntries(r)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ordinary directory: %v %v", entries, err)
	}
	if _, err := readLoraDatasetZIPEntry(entries["images/photo.png"], 2); err == nil {
		t.Fatal("entry limit ignored")
	}
	r.File[1].SetMode(os.ModeSymlink | 0777)
	if _, err := loraDatasetZIPEntries(r); err == nil {
		t.Fatal("symlink accepted")
	}
	r.File[1].SetMode(0600)
	r.File[1].UncompressedSize64 = uint64(maxLoraTrainingImageBytes + 1)
	if _, err := loraDatasetZIPEntries(r); err == nil {
		t.Fatal("oversize declaration accepted")
	}
}

func assertLoraDatasetZIPRoundTrip(t *testing.T, ctx context.Context, app *App, userID, foreignID int64, original loraDatasetView, session, csrf string) {
	t.Helper()
	call := func(owner int64, method, target string, body []byte, contentType string, revision int64, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, bytes.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("X-Dataset-Revision", strconv.FormatInt(revision, 10))
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: owner, Role: "user", CanTrainImageLora: true}))
		w := httptest.NewRecorder()
		app.handleLoraDatasets(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, target, w.Code, want, w.Body.String())
		}
		return w
	}
	base := "/api/lora-datasets/" + original.Dataset.ID
	call(foreignID, "GET", base+"/export", nil, "", 0, 404)
	export := call(userID, "GET", base+"/export", nil, "", 0, 200)
	if export.Header().Get("Content-Type") != "application/zip" {
		t.Fatal("wrong download type")
	}
	archive, err := zip.NewReader(bytes.NewReader(export.Body.Bytes()), int64(export.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 5 {
		t.Fatalf("export lost excluded image: %d entries", len(archive.File))
	}
	entries, err := loraDatasetZIPEntries(archive)
	if err != nil {
		t.Fatal(err)
	}
	caption, err := readLoraDatasetZIPEntry(entries["images/0001.txt"], 4000)
	if err != nil || string(caption) != original.Manifest.Images[0].Caption {
		t.Fatalf("caption changed: %q %v", caption, err)
	}
	manifest := domain.LoraDatasetManifest{Version: 1, Images: []domain.LoraDatasetImage{}}
	cipher, _, err := app.encryptLoraDatasetManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.store.CreateLoraDataset(ctx, userID, newRequestID(), "ZIP target", cipher)
	if err != nil {
		t.Fatal(err)
	}
	target := "/api/lora-datasets/" + row.ID
	importZIP := func(data []byte, revision int64, want int) loraDatasetView {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("archive", "dataset.zip")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		response := call(userID, "POST", target+"/import", body.Bytes(), writer.FormDataContentType(), revision, want)
		var view loraDatasetView
		if want == 200 {
			if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
		}
		return view
	}
	view := importZIP(export.Body.Bytes(), row.Revision, 200)
	if !reflect.DeepEqual(view.Manifest.Settings, original.Manifest.Settings) || len(view.Manifest.Images) != 2 {
		t.Fatal("ZIP lost settings or images")
	}
	for i, image := range view.Manifest.Images {
		before := original.Manifest.Images[i]
		if image.AssetID != before.AssetID || image.Caption != before.Caption || image.Excluded != before.Excluded || image.ID == before.ID {
			t.Fatalf("ZIP changed image %d: %+v", i, image)
		}
	}
	importZIP(export.Body.Bytes(), row.Revision, 409)
	importZIP([]byte("broken archive"), view.Dataset.Revision, 400)
	importZIP(datasetTestZIP(t, []string{"a.png", "a.txt"}, [][]byte{[]byte("broken image"), []byte("caption")}), view.Dataset.Revision, 400)
	unchanged, err := app.store.LoraDataset(ctx, userID, row.ID)
	if err != nil || unchanged.Revision != view.Dataset.Revision {
		t.Fatalf("failed import mutated working set: %+v %v", unchanged, err)
	}
	imageReader, err := entries["images/0001.png"].Open()
	if err != nil {
		t.Fatal(err)
	}
	image, err := io.ReadAll(imageReader)
	imageReader.Close()
	if err != nil {
		t.Fatal(err)
	}
	generic := datasetTestZIP(t, []string{"b.png", "b.txt", "a.png"}, [][]byte{image, []byte("  manual generic caption\n"), image})
	view = importZIP(generic, view.Dataset.Revision, 200)
	if view.Manifest.Images[0].Caption != "" || view.Manifest.Images[1].Caption != "  manual generic caption\n" || view.Manifest.Settings.TriggerWord != original.Manifest.Settings.TriggerWord {
		t.Fatal("plain ZIP order, captions or settings changed")
	}
	if err := app.store.DeleteLoraDataset(ctx, userID, row.ID, view.Dataset.Revision); err != nil {
		t.Fatal(err)
	}
	if files, err := os.ReadDir(app.mediaSpoolDir()); err != nil || len(files) != 0 {
		t.Fatalf("ZIP temporary files leaked: %v %v", files, err)
	}
}
