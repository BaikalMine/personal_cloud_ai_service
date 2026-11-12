package trainingagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

func (controller *Controller) executionProfile(record *jobRecord) (ProfileConfig, error) {
	if record == nil {
		return ProfileConfig{}, os.ErrNotExist
	}
	if record.ProfileSnapshot != nil {
		if record.ProfileSnapshot.ID != record.Spec.ProfileID {
			return ProfileConfig{}, errors.New("recorded training profile identity differs from job")
		}
		return *record.ProfileSnapshot, nil
	}
	// Legacy jobs have no submission snapshot. Capture the actually selected
	// execution profile now, without pretending it was recorded at submission.
	profile, ok := controller.config.Profile(record.Spec.ProfileID)
	if !ok {
		return ProfileConfig{}, errors.New("training profile is no longer available")
	}
	return profile, nil
}

func trainingFileIdentity(ctx context.Context, filename string) (domain.LoraFileIdentity, error) {
	var identity domain.LoraFileIdentity
	file, err := os.Open(filename)
	if err != nil {
		return identity, errors.New("provenance source is unavailable")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > 512<<30 {
		return identity, errors.New("invalid provenance source")
	}
	bytes, digest, err := backupDigest(ctx, io.Discard, file)
	if err != nil {
		return identity, err
	}
	after, err := file.Stat()
	current, currentErr := os.Stat(filename)
	if err != nil || currentErr != nil || bytes != before.Size() || after.Size() != before.Size() ||
		!after.ModTime().Equal(before.ModTime()) || !os.SameFile(before, current) ||
		current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) {
		return identity, errors.New("provenance source changed during hashing")
	}
	identity = domain.LoraFileIdentity{Name: filepath.Base(filename), Bytes: bytes, SHA256: digest}
	if !identity.Valid() {
		return domain.LoraFileIdentity{}, errors.New("invalid provenance source identity")
	}
	return identity, nil
}

func tunerSourceFile(name string) bool {
	name = strings.ToLower(name)
	return strings.HasSuffix(name, ".py") || name == "pyproject.toml" || name == "uv.lock" ||
		name == "poetry.lock" || name == "setup.cfg" || strings.HasPrefix(name, "requirements") && strings.HasSuffix(name, ".txt")
}

// Hash executable Python sources and dependency declarations, not model caches,
// virtual environments or host paths. The scope is explicit in the passport.
func tunerSourceIdentity(ctx context.Context, root string) (domain.LoraTunerIdentity, error) {
	identity := domain.LoraTunerIdentity{SourceScope: "python-and-dependencies-v1"}
	var files []string
	var total int64
	pythonFiles := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("tuner source is unavailable")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			switch strings.ToLower(entry.Name()) {
			case ".git", ".venv", "venv", "env", "__pycache__", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(path)
			if err != nil || info.IsDir() || tunerSourceFile(entry.Name()) {
				return errors.New("linked tuner source cannot be identified")
			}
		}
		if !tunerSourceFile(entry.Name()) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 || len(files) >= 20000 {
			return errors.New("invalid or oversized tuner source tree")
		}
		total += info.Size()
		if total > 256<<20 {
			return errors.New("oversized tuner source tree")
		}
		files = append(files, path)
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".py") {
			pythonFiles++
		}
		return nil
	})
	if err != nil || pythonFiles == 0 {
		return identity, errors.Join(err, errors.New("cannot identify tuner source"))
	}
	sort.Strings(files)
	digest := sha256.New()
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return identity, err
		}
		// Empty __init__.py is a legitimate part of the source tree.
		file, err := os.Open(path)
		if err != nil {
			return identity, errors.New("tuner source changed during capture")
		}
		before, statErr := file.Stat()
		n, hash, copyErr := backupDigest(ctx, io.Discard, io.LimitReader(file, (64<<20)+1))
		after, afterErr := file.Stat()
		_ = file.Close()
		if statErr != nil || copyErr != nil || afterErr != nil || n != before.Size() ||
			after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
			return identity, errors.Join(copyErr, errors.New("tuner source changed during capture"))
		}
		fmt.Fprintf(digest, "%s\x00%d\x00%s\n", filepath.ToSlash(rel), n, hash)
	}
	identity.SourceSHA256 = hex.EncodeToString(digest.Sum(nil))
	gitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(gitCtx, "git", append([]string{"-C", root}, args...)...)
		cmd.WaitDelay = time.Second
		return cmd.Output()
	}
	// Git is optional on the worker. A source digest remains useful without it.
	if revision, err := git("rev-parse", "--verify", "HEAD"); err == nil {
		if status, err := git("status", "--porcelain", "--untracked-files=normal"); err == nil {
			value := strings.TrimSpace(string(revision))
			_, decodeErr := hex.DecodeString(value)
			if (len(value) == 40 || len(value) == 64) && decodeErr == nil {
				dirty := len(status) != 0
				identity.Revision, identity.Dirty = value, &dirty
			}
		}
	}
	return identity, nil
}

type provenanceSources struct {
	models  []string
	dataset string
	tuner   string
}

func captureTrainingProvenance(ctx context.Context, record *jobRecord, profile ProfileConfig, config Config, modelPath string) (*domain.LoraTrainingProvenance, provenanceSources, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	sources := provenanceSources{models: []string{profile.DiT, modelPath, profile.VAE, profile.TextEncoder}, dataset: record.DatasetPath, tuner: config.TunerDir}
	public := profile.ExecutionIdentity()
	spec := record.Spec
	provenance := &domain.LoraTrainingProvenance{Version: 1, GatewayJobID: spec.GatewayJobID,
		CapturedAt: time.Now().UTC(), Profile: public, ProfileSHA256: public.Hash(),
		Parameters: domain.LoraExecutionParameters{Resolution: spec.Resolution, MaxSteps: spec.MaxSteps,
			NetworkDim: spec.NetworkDim, NetworkAlpha: spec.NetworkAlpha, LearningRate: spec.LearningRate,
			Seed: spec.Seed, SampleCount: spec.SampleCount}}
	identities := make(map[string]domain.LoraFileIdentity)
	for i, role := range []string{"base_source", "base_execution", "vae", "text_encoder"} {
		key := backupFileKey(sources.models[i])
		identity, ok := identities[key]
		if !ok {
			var err error
			identity, err = trainingFileIdentity(ctx, sources.models[i])
			if err != nil {
				return nil, sources, fmt.Errorf("identify %s: %w", role, err)
			}
			identities[key] = identity
		}
		provenance.Models = append(provenance.Models, domain.LoraModelIdentity{Role: role, LoraFileIdentity: identity})
	}
	var err error
	provenance.DatasetArchive, err = trainingFileIdentity(ctx, sources.dataset)
	if err != nil {
		return nil, sources, err
	}
	provenance.Tuner, err = tunerSourceIdentity(ctx, sources.tuner)
	if err != nil {
		return nil, sources, err
	}
	return provenance, sources, provenance.Validate()
}

func verifyTrainingProvenance(ctx context.Context, provenance *domain.LoraTrainingProvenance, sources provenanceSources) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	identities := make(map[string]domain.LoraFileIdentity)
	for i, expected := range provenance.Models {
		key := backupFileKey(sources.models[i])
		current, ok := identities[key]
		var err error
		if !ok {
			current, err = trainingFileIdentity(ctx, sources.models[i])
			identities[key] = current
		}
		if err != nil || current != expected.LoraFileIdentity {
			return fmt.Errorf("%s changed since provenance capture: %w", expected.Role, errors.Join(err, domain.ErrLoraProvenance))
		}
	}
	dataset, err := trainingFileIdentity(ctx, sources.dataset)
	if err != nil || dataset != provenance.DatasetArchive {
		return errors.Join(err, errors.New("training archive changed since provenance capture"))
	}
	tuner, err := tunerSourceIdentity(ctx, sources.tuner)
	if err != nil || tuner.SourceSHA256 != provenance.Tuner.SourceSHA256 || tuner.Revision != provenance.Tuner.Revision ||
		(tuner.Dirty == nil) != (provenance.Tuner.Dirty == nil) || tuner.Dirty != nil && *tuner.Dirty != *provenance.Tuner.Dirty {
		return errors.Join(err, errors.New("tuner changed since provenance capture"))
	}
	return nil
}

func (controller *Controller) persistProvenance(id string, provenance *domain.LoraTrainingProvenance) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	record := controller.jobs[id]
	if record == nil {
		return os.ErrNotExist
	}
	merged, err := domain.MergeLoraProvenance(record.Status.Provenance, provenance)
	if err != nil {
		return err
	}
	copy := cloneRecord(record)
	copy.Status.Provenance = merged
	if err := controller.persist(copy); err != nil {
		return err
	}
	record.Status.Provenance = merged
	return nil
}
