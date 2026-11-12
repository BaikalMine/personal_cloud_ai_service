package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/store"
)

func TestEnhancementSnapshotThroughDurableExecutor(t *testing.T) {
	f := newFixture(t)
	base, _ := url.Parse("http://preparation-only.invalid")
	assistant := promptassistant.NewClient(base, "saved-text").WithVisionModel("saved-vision", time.Minute, "0")
	refs := []promptassistant.ImageReference{
		{Number: 1, Role: promptassistant.ImageReferenceWardrobeObject, MIMEType: "image/png", Image: []byte("vase"), Correction: "The vase is green."},
		{Number: 2, Role: promptassistant.ImageReferenceBackground, MIMEType: "image/png", Image: []byte("room")},
		{Number: 3, Role: promptassistant.ImageReferenceStyle, MIMEType: "image/png", Image: []byte("painting")},
		{Number: 4, Role: promptassistant.ImageReferenceDetails, MIMEType: "image/png", Image: []byte("texture")},
	}
	prepared, err := assistant.PrepareEnhancement(promptassistant.ModeTextToVideo, promptassistant.ProfileMiniMaxH3REF2VA, "Move the vase across the room", refs,
		promptassistant.VideoContext{Mode: promptassistant.VideoModeREF2VA, DurationSeconds: 15, ImageCount: 4, AudioReference: true, VideoReference: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	expectedBody := append([]byte(nil), prepared.Body...)
	metadata, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	metadataCipher, err := f.cipher.EncryptBytes(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	response := []byte(`{"done":true,"message":{"content":"{\"prompt\":\"The green vase moves across the room.\",\"references\":[{\"id\":\"Picture 1\",\"summary\":\"Green vase\"},{\"id\":\"Picture 2\",\"summary\":\"A room\"},{\"id\":\"Picture 3\",\"summary\":\"A painting\"},{\"id\":\"Picture 4\",\"summary\":\"A texture\"}]}"},"eval_count":35}`)
	w := f.worker(t, func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(body, expectedBody) {
			t.Errorf("executor changed the prepared request: %v", err)
		}
		rw.Write(response)
	})
	client := NewClient(f.store, f.cipher)
	job, err := client.Submit(context.Background(), f.work, prepared.Body, prepared.Policy.Timeout)
	if err != nil {
		t.Fatal(err)
	}
	clear(prepared.Body)
	for _, ref := range refs {
		clear(ref.Image)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = client.Wait(ctx, job.ID, job.LeaseToken); !errors.Is(err, context.Canceled) {
		t.Fatalf("disconnected client: %v", err)
	}
	if worked, err := w.RunOne(context.Background()); err != nil || !worked {
		t.Fatalf("executor: %v %v", worked, err)
	}
	client = NewClient(store.New(f.db), f.cipher)
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		saved, body, err := client.Wait(ctx, job.ID, job.LeaseToken)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		plain, err := f.cipher.DecryptBytes(metadataCipher)
		if err != nil {
			t.Fatal(err)
		}
		var restored promptassistant.PreparedEnhancement
		if err = json.Unmarshal(plain, &restored); err != nil {
			t.Fatal(err)
		}
		clear(plain)
		output, err := promptassistant.ParseEnhancementResponse(restored, saved.ResponseStatus, body)
		clear(body)
		if err != nil || output.Prompt != "The green vase moves across the room." || output.Model != "saved-vision" || output.Usage.CompletionTokens != 35 || len(output.References) != 6 {
			t.Fatalf("snapshot recovery lost data: %+v %v", output, err)
		}
		for i, ref := range output.References {
			if i < 4 && (!ref.Inspected || ref.Role != string(refs[i].Role)) {
				t.Fatalf("photo lost its selected role: %+v", ref)
			}
			if i >= 4 && (ref.Inspected || !reflect.DeepEqual(ref, restored.References[i])) {
				t.Fatalf("unseen media acquired fabricated observations: %+v", ref)
			}
		}
	}
	if worked, err := w.RunOne(context.Background()); err != nil || worked || calls.Load() != 1 {
		t.Fatalf("retrieval repeated the model call: worked=%v calls=%d err=%v", worked, calls.Load(), err)
	}
}
