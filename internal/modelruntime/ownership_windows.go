//go:build windows

package modelruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"ai-access-gateway/internal/domain"
	"golang.org/x/sys/windows/registry"
)

type ownerState struct {
	Version int                         `json:"version"`
	Owner   domain.GPUInferenceExecutor `json:"owner"`
}

func runtimeIdentity(directory string) (string, error) {
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", err
	}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", err
	}
	defer key.Close()
	host, _, err := key.GetStringValue("MachineGuid")
	if err != nil || strings.TrimSpace(host) == "" {
		return "", errors.New("Windows installation identity is unavailable")
	}
	digest := sha256.Sum256([]byte("owned-model/v1\x00" + strings.ToLower(strings.TrimSpace(host)) + "\x00" + strings.ToLower(filepath.Clean(canonical))))
	return hex.EncodeToString(digest[:]), nil
}

// A generation is issued only while exclusively owning and draining this
// installation/path's process group. It survives process crashes, not disk loss.
func (r *Runtime) Advance(ctx context.Context) (domain.GPUInferenceExecutor, error) {
	if !r.mu.TryLock() {
		return domain.GPUInferenceExecutor{}, ErrBusy
	}
	defer r.mu.Unlock()
	if err := r.lifetime.Err(); err != nil {
		return domain.GPUInferenceExecutor{}, err
	}
	stopCtx, cancel := context.WithTimeout(ctx, r.config.StopTimeout)
	_, err := r.stopHTTPLocked(stopCtx, r.retainedDone)
	cancel()
	if err != nil {
		return domain.GPUInferenceExecutor{}, err
	}
	path := filepath.Join(r.config.StateDirectory, "model-owner.json")
	state := ownerState{Version: 1, Owner: domain.GPUInferenceExecutor{ID: r.identity}}
	file, err := os.Open(path)
	if err == nil {
		decoder := json.NewDecoder(io.LimitReader(file, 4096))
		decoder.DisallowUnknownFields()
		var tail any
		err = decoder.Decode(&state)
		if err == nil {
			if decoder.Decode(&tail) != io.EOF {
				err = errors.New("invalid model owner state")
			}
		}
		file.Close()
		if err != nil {
			return domain.GPUInferenceExecutor{}, err
		}
		if state.Version != 1 || state.Owner.ID != r.identity || state.Owner.Generation < 1 || state.Owner.Generation == math.MaxInt64 || len(state.Owner.Token) != 64 {
			return domain.GPUInferenceExecutor{}, errors.New("model owner state does not match this installation/path")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.GPUInferenceExecutor{}, err
	}
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		return domain.GPUInferenceExecutor{}, err
	}
	state.Owner.Generation++
	state.Owner.Token = hex.EncodeToString(token[:])
	data, err := json.Marshal(state)
	if err != nil {
		return domain.GPUInferenceExecutor{}, err
	}
	file, err = os.CreateTemp(r.config.StateDirectory, "model-owner-*.tmp")
	if err != nil {
		return domain.GPUInferenceExecutor{}, err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return domain.GPUInferenceExecutor{}, err
	}
	if err = os.Rename(temporary, path); err != nil {
		return domain.GPUInferenceExecutor{}, err
	}
	return state.Owner, nil
}
