package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/componentinspection"
	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mediaprobe"
	"ai-access-gateway/internal/store"
)

func postprocessQuoteFixture(t *testing.T, video bool) (*App, postprocessQuote) {
	t.Helper()
	cipher, err := contentcrypto.NewCipher("postprocess-quote-fixture-secret-at-least-32")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{contentCipher: cipher, cfg: Config{SessionSecret: "postprocess-quote-fixture-secret-at-least-32"}}
	facts, options := postprocessTestSource(), postprocessTestOptions()
	mime, family := "image/png", domain.PostprocessImageTemplate
	if video {
		facts = postprocessVideoSource()
		options.Interpolate, options.RIFECheckpoint = 2, "rife49.pth"
		mime, family = "video/mp4", domain.PostprocessVideoTemplate
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	q := postprocessQuote{Purpose: postprocessQuotePurpose, Version: 1, UserID: 9007199254740993, RequestID: "postprocess-quote-test-0001",
		CreatedAt: now, ExpiresAt: now.Add(postprocessQuoteTTL), Policy: currentPostprocessWorkloadPolicy(), Cost: 1, TemplateID: family}
	q.Namespace = comfyUploadNamespace(a.comfyClientID(q.UserID))
	id := domain.PostprocessSourceIdentity{JobID: 9007199254740995, JobPublicID: "postprocess-parent-test-0001", MediaID: 9007199254740997,
		MediaType: facts.MediaType, MIMEType: mime, SizeBytes: 1234, SHA256: strings.Repeat("a", 64), ArchiveSHA256: strings.Repeat("a", 64)}
	m := mediaprobe.DecodedMeasurement{Measurement: mediaprobe.Measurement{Version: 1, Source: mediaprobe.FullDecodeSource, SHA256: id.SHA256, SizeBytes: id.SizeBytes,
		MeasuredAt: now, MediaType: facts.MediaType, Width: facts.Width, Height: facts.Height}, DecodeVersion: mediaprobe.FullDecodeVersion,
		Frames: facts.Frames, FPSNumerator: facts.FPSNumerator, FPSDenominator: facts.FPSDenominator, DecodedPixels: int64(facts.Width * facts.Height * facts.Frames),
		Opaque: true, NormalOrientation: true, SquarePixels: true, UniformCadence: facts.ConstantFrameRate}
	q.Sources = []postprocessQuoteSource{{id, m, postprocessQuoteInputName(q.Namespace, q.RequestID, 0, id)}}
	graph, plan, err := buildPostprocessPrompt(facts, options, q.Sources[0].InputName, "", q.Namespace, postprocessTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	q.Plan = plan
	q.Schemas, err = postprocessBoundSchemas(postprocessTestCatalog(), graph)
	if err != nil {
		t.Fatal(err)
	}
	q.SchemaSHA256, _ = postprocessJSONDigest(q.Schemas)
	q.GraphSHA256, _ = postprocessJSONDigest(graph)
	if video {
		r, err := generationComponentRequest(q.RequestID, graph)
		if err != nil {
			t.Fatal(err)
		}
		q.Components = &componentinspection.Response{Version: 1, RequestID: r.RequestID, RequestSHA256: r.Digest(), Source: componentinspection.Source, State: "complete"}
		for _, ref := range r.References {
			q.Components.Items = append(q.Components.Items, componentinspection.Observation{Reference: ref, State: "verified", Bytes: 123456, SHA256: strings.Repeat("b", 64), HashedAt: now, CheckedAt: now})
		}
		q.FilesSHA256, _ = generationRepeatFilesDigest(q.Components)
	}
	return a, q
}

func postprocessTestReceipt(t *testing.T, a *App, q postprocessQuote) domain.GenerationPostprocessReceipt {
	t.Helper()
	cipher, digest, err := a.encodePostprocessQuote(q)
	if err != nil {
		t.Fatal(err)
	}
	return domain.GenerationPostprocessReceipt{UserID: q.UserID, RequestID: q.RequestID, Version: 1, State: "quoted", PayloadCipher: cipher,
		ConfirmationSHA256: digest, Sources: postprocessQuoteSources(q), ExpiresAt: q.ExpiresAt}
}

func TestPostprocessQuoteRoundTrip(t *testing.T) {
	for _, video := range []bool{false, true} {
		a, q := postprocessQuoteFixture(t, video)
		r := postprocessTestReceipt(t, a, q)
		decoded, err := a.decodePostprocessQuote(r)
		if err != nil || !reflect.DeepEqual(q, decoded) {
			t.Fatalf("roundtrip video=%v: %v", video, err)
		}
		plain, _ := a.contentCipher.DecryptBytesWithAAD(r.PayloadCipher, postprocessQuoteAAD(q.UserID, q.RequestID))
		if !bytes.Contains(plain, []byte(`"user_id":"9007199254740993"`)) || !bytes.Contains(plain, []byte(`"media_id":"9007199254740997"`)) {
			t.Fatal("ID precision lost")
		}
		for _, forbidden := range []string{`"prompt":`, `"seed":`, `"sampler":`, `"graph":`} {
			if bytes.Contains(plain, []byte(forbidden)) {
				t.Fatal("ordinary generation leaked into quote", forbidden)
			}
		}
		graph, err := validatePostprocessQuote(decoded)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := graph["6"]; !ok {
			t.Fatal("no fixed save sink")
		}
	}
}

func TestPostprocessQuoteRejectsSemanticChanges(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*postprocessQuote)
	}{
		{"purpose", func(q *postprocessQuote) { q.Purpose = "generation" }},
		{"version", func(q *postprocessQuote) { q.Version++ }},
		{"owner", func(q *postprocessQuote) { q.UserID = 0 }},
		{"request", func(q *postprocessQuote) { q.RequestID = "x" }},
		{"expiry", func(q *postprocessQuote) { q.ExpiresAt = q.ExpiresAt.Add(time.Second) }},
		{"submicrosecond", func(q *postprocessQuote) {
			q.CreatedAt = q.CreatedAt.Add(time.Nanosecond)
			q.ExpiresAt = q.CreatedAt.Add(postprocessQuoteTTL)
		}},
		{"cost", func(q *postprocessQuote) { q.Cost = 0 }},
		{"family", func(q *postprocessQuote) { q.TemplateID = domain.PostprocessVideoTemplate }},
		{"policy", func(q *postprocessQuote) { q.Policy.MaxScale = 4 }},
		{"source hash", func(q *postprocessQuote) { q.Sources[0].Identity.SHA256 = strings.Repeat("b", 64) }},
		{"archive hash", func(q *postprocessQuote) { q.Sources[0].Identity.ArchiveSHA256 = strings.Repeat("b", 64) }},
		{"mime", func(q *postprocessQuote) { q.Sources[0].Identity.MIMEType = "image/svg+xml" }},
		{"metadata only", func(q *postprocessQuote) { q.Sources[0].Measurement.DecodeVersion = 0 }},
		{"pixels", func(q *postprocessQuote) { q.Sources[0].Measurement.DecodedPixels++ }},
		{"old decode", func(q *postprocessQuote) { q.Sources[0].Measurement.MeasuredAt = q.CreatedAt.Add(-time.Hour) }},
		{"alpha", func(q *postprocessQuote) { q.Sources[0].Measurement.Opaque = false }},
		{"namespace", func(q *postprocessQuote) { q.Sources[0].InputName = "other/asset.png" }},
		{"changed operation", func(q *postprocessQuote) { q.Plan.Options.RTXQuality = "LOW" }},
		{"frame count", func(q *postprocessQuote) { q.Plan.OutputFrames++ }},
		{"graph digest", func(q *postprocessQuote) { q.GraphSHA256 = strings.Repeat("0", 64) }},
		{"schema digest", func(q *postprocessQuote) { q.SchemaSHA256 = strings.Repeat("0", 64) }},
		{"extra node", func(q *postprocessQuote) { q.Schemas["KSampler"] = comfyNodeSchema{ClassType: "KSampler"} }},
		{"schema module", func(q *postprocessQuote) {
			n := q.Schemas["SaveImage"]
			n.PythonModule = "replacement"
			q.Schemas["SaveImage"] = n
		}},
		{"missing reference", func(q *postprocessQuote) { q.Plan.Options.ColorMethod = "adain"; q.Plan.Options.ColorStrength = 1 }},
		{"unused reference", func(q *postprocessQuote) { q.Sources = append(q.Sources, q.Sources[0]) }},
		{"invented component attestation", func(q *postprocessQuote) { q.Components = &componentinspection.Response{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, q := postprocessQuoteFixture(t, false)
			tc.edit(&q)
			if _, err := validatePostprocessQuote(q); err == nil {
				t.Fatal("accepted mutation")
			}
		})
	}
}

func TestPostprocessQuoteCipherBindingAndStrictJSON(t *testing.T) {
	a, q := postprocessQuoteFixture(t, false)
	for _, tc := range []struct {
		name string
		edit func(*domain.GenerationPostprocessReceipt)
	}{
		{"owner", func(r *domain.GenerationPostprocessReceipt) { r.UserID++ }},
		{"request", func(r *domain.GenerationPostprocessReceipt) { r.RequestID += "x" }},
		{"version", func(r *domain.GenerationPostprocessReceipt) { r.Version++ }},
		{"expires", func(r *domain.GenerationPostprocessReceipt) { r.ExpiresAt = r.ExpiresAt.Add(time.Second) }},
		{"sources", func(r *domain.GenerationPostprocessReceipt) { r.Sources[0].MediaID++ }},
		{"digest", func(r *domain.GenerationPostprocessReceipt) { r.ConfirmationSHA256 = strings.Repeat("c", 64) }},
		{"cipher", func(r *domain.GenerationPostprocessReceipt) { r.PayloadCipher[10] ^= 1 }},
		{"oversized", func(r *domain.GenerationPostprocessReceipt) {
			r.PayloadCipher = make([]byte, postprocessQuoteMaxBytes+129)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := postprocessTestReceipt(t, a, q)
			tc.edit(&r)
			if _, err := a.decodePostprocessQuote(r); err == nil {
				t.Fatal("accepted replacement")
			}
		})
	}
	data, _ := json.Marshal(q)
	for _, plain := range [][]byte{
		append(append([]byte{}, data...), []byte(` {}`)...),
		bytes.Replace(data, []byte(`"purpose":`), []byte(`"unknown":1,"purpose":`), 1),
		bytes.Replace(data, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		append([]byte(" "), data...),
	} {
		r := postprocessTestReceipt(t, a, q)
		r.PayloadCipher, _ = a.contentCipher.EncryptBytesWithAAD(plain, postprocessQuoteAAD(q.UserID, q.RequestID))
		r.ConfirmationSHA256 = generationEvidenceDigest(plain)
		if _, err := a.decodePostprocessQuote(r); err == nil {
			t.Fatal("noncanonical or extended JSON accepted")
		}
	}
	r := postprocessTestReceipt(t, a, q)
	r.PayloadCipher, _ = a.contentCipher.EncryptBytes(data)
	if _, err := a.decodePostprocessQuote(r); err == nil {
		t.Fatal("generic generation ciphertext accepted")
	}
}

func TestPostprocessQuoteSchemaScope(t *testing.T) {
	_, q := postprocessQuoteFixture(t, false)
	graph, err := validatePostprocessQuote(q)
	if err != nil {
		t.Fatal(err)
	}
	c := postprocessTestCatalog()
	n := c.Nodes["LoadImage"]
	n.DisplayName, n.Category, n.SchemaFingerprint = "new label", "new category", "different"
	i := n.Required["image"]
	i.Choices = []any{"other-upload.png"}
	i.Default = "other-upload.png"
	n.Required["image"] = i
	c.Nodes["LoadImage"] = n
	c.Nodes["Unrelated"] = comfyNodeSchema{ClassType: "Unrelated"}
	before, _ := json.Marshal(c)
	bound, err := postprocessBoundSchemas(c, graph)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := postprocessJSONDigest(bound)
	if digest != q.SchemaSHA256 {
		t.Fatal("display/upload/unrelated drift invalidates quote")
	}
	after, _ := json.Marshal(c)
	if !bytes.Equal(before, after) {
		t.Fatal("shared live catalog mutated")
	}
	n = c.Nodes["RTXVideoSuperResolution"]
	n.PythonModule = "changed"
	c.Nodes["RTXVideoSuperResolution"] = n
	bound, _ = postprocessBoundSchemas(c, graph)
	digest, _ = postprocessJSONDigest(bound)
	if digest == q.SchemaSHA256 {
		t.Fatal("operational schema drift ignored")
	}
}

func TestPostprocessQuoteRIFERequiresCurrentVerifiedFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*postprocessQuote)
	}{
		{"absent", func(q *postprocessQuote) { q.Components = nil }},
		{"pending", func(q *postprocessQuote) { q.Components.State = "pending" }},
		{"missing", func(q *postprocessQuote) { q.Components.Items[0].State = "missing" }},
		{"empty", func(q *postprocessQuote) { q.Components.Items[0].Bytes = 0 }},
		{"old", func(q *postprocessQuote) {
			q.Components.Items[0].CheckedAt = q.CreatedAt.Add(-2 * time.Minute)
			q.Components.Items[0].HashedAt = q.CreatedAt.Add(-3 * time.Minute)
		}},
		{"wrong request", func(q *postprocessQuote) { q.Components.RequestID += "x" }},
		{"wrong file", func(q *postprocessQuote) { q.Components.Items[0].Name = "rife47.pth" }},
		{"digest", func(q *postprocessQuote) { q.FilesSHA256 = strings.Repeat("a", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, q := postprocessQuoteFixture(t, true)
			tc.edit(&q)
			if _, err := validatePostprocessQuote(q); err == nil {
				t.Fatal("invalid file observation accepted")
			}
		})
	}
}

func TestPostprocessQuoteRetryAndAcceptedExpiry(t *testing.T) {
	a, q := postprocessQuoteFixture(t, false)
	q.CreatedAt = q.CreatedAt.Add(-time.Hour)
	q.ExpiresAt = q.CreatedAt.Add(postprocessQuoteTTL)
	q.Sources[0].Measurement.MeasuredAt = q.CreatedAt
	r := postprocessTestReceipt(t, a, q)
	request := postprocessQuoteRequest{RequestID: q.RequestID, SourceJobID: q.Sources[0].Identity.JobPublicID, SourceMediaID: q.Sources[0].Identity.MediaID, Options: q.Plan.Options}
	if _, err := a.decodePostprocessQuote(r); err != nil {
		t.Fatal("expired quote cannot be inspected for accepted-job recovery", err)
	}
	if _, err := a.existingPostprocessQuote(r, request); !errors.Is(err, store.ErrPostprocessExpired) {
		t.Fatal("expired unaccepted quote reused", err)
	}
	r.State = "accepted"
	if _, err := a.existingPostprocessQuote(r, request); err != nil {
		t.Fatal("accepted receipt lost after deadline", err)
	}
	request.Options.RTXQuality = "LOW"
	if _, err := a.existingPostprocessQuote(r, request); !errors.Is(err, store.ErrPostprocessConflict) {
		t.Fatal("same request ID replaced intent", err)
	}
	r.State = "cancelled"
	if _, err := a.existingPostprocessQuote(r, request); !errors.Is(err, store.ErrPostprocessConflict) {
		t.Fatal("tombstone recreated", err)
	}
}
