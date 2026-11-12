package store

import (
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
)

func TestPostprocessSourceIdentityValidation(t *testing.T) {
	source := domain.PostprocessSourceIdentity{JobID: 1, JobPublicID: "postprocess-parent-0001", MediaID: 2, MediaType: "image", MIMEType: "image/png", SizeBytes: 100, SHA256: strings.Repeat("a", 64)}
	if !postprocessSourcesValid([]domain.PostprocessSourceIdentity{source}) {
		t.Fatal("legacy empty archive hash rejected")
	}
	for name, mutate := range map[string]func(*domain.PostprocessSourceIdentity){
		"job":           func(s *domain.PostprocessSourceIdentity) { s.JobID = 0 },
		"media":         func(s *domain.PostprocessSourceIdentity) { s.MediaID = -1 },
		"public":        func(s *domain.PostprocessSourceIdentity) { s.JobPublicID = "short" },
		"size":          func(s *domain.PostprocessSourceIdentity) { s.SizeBytes = 512<<20 + 1 },
		"empty":         func(s *domain.PostprocessSourceIdentity) { s.SizeBytes = 0 },
		"hash":          func(s *domain.PostprocessSourceIdentity) { s.SHA256 = strings.Repeat("A", 64) },
		"archive drift": func(s *domain.PostprocessSourceIdentity) { s.ArchiveSHA256 = strings.Repeat("b", 64) },
		"media family":  func(s *domain.PostprocessSourceIdentity) { s.MediaType = "audio" },
		"mime family":   func(s *domain.PostprocessSourceIdentity) { s.MIMEType = "video/mp4" },
	} {
		t.Run(name, func(t *testing.T) {
			s := source
			mutate(&s)
			if postprocessSourcesValid([]domain.PostprocessSourceIdentity{s}) {
				t.Fatal("invalid identity accepted")
			}
		})
	}
	if postprocessSourcesValid(nil) || postprocessSourcesValid([]domain.PostprocessSourceIdentity{source, source}) {
		t.Fatal("empty or duplicate sources")
	}
	ref := source
	ref.MediaID++
	ref.MediaType = "video"
	ref.MIMEType = "video/mp4"
	if postprocessSourcesValid([]domain.PostprocessSourceIdentity{source, ref}) {
		t.Fatal("video color reference")
	}
	if quickGenerationQuotaKind(domain.PostprocessVideoTemplate) != QuickGenerationVideoQuota || quickGenerationQuotaKind(domain.PostprocessImageTemplate) != QuickGenerationImageQuota {
		t.Fatal("postprocess quota family")
	}
}
