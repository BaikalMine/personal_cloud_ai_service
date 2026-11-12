package componentinspection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var ErrBusy = errors.New("component inspection capacity reached")

const cacheLifetime = 10 * time.Minute
const failedLifetime = 5 * time.Second
const maxCacheEntries = 256

type entry struct {
	ref      Reference
	result   Observation
	info     os.FileInfo
	finished time.Time
}

type Inspector struct {
	roots  map[string][]string
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	cache  map[string]*entry
	queue  chan *entry
	wg     sync.WaitGroup
}

func New(roots map[string][]string) (*Inspector, error) {
	copyRoots := map[string][]string{}
	for kind, paths := range roots {
		if !ValidKind(kind) || len(paths) == 0 || len(paths) > 8 {
			return nil, errors.New("invalid component directory mapping")
		}
		for _, path := range paths {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path {
				return nil, errors.New("component directories must be clean absolute paths")
			}
			copyRoots[kind] = append(copyRoots[kind], path)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	i := &Inspector{roots: copyRoots, ctx: ctx, cancel: cancel, cache: map[string]*entry{}, queue: make(chan *entry, MaxReferences)}
	i.wg.Add(1)
	go i.worker()
	return i, nil
}

func (i *Inspector) Close() { i.cancel(); i.wg.Wait() }

// Inspect starts bounded read-only work. A restart discards the digest cache;
// the next request hashes again instead of trusting serialized stat metadata.
func (i *Inspector) Inspect(request Request) (Response, error) {
	response := Response{Version: 1, RequestID: request.RequestID, RequestSHA256: request.Digest(), Source: Source, State: "complete", Items: []Observation{}}
	if !request.Valid() {
		return response, errors.New("invalid component inspection request")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.ctx.Err() != nil {
		return response, i.ctx.Err()
	}
	now := time.Now().UTC()
	for key, cached := range i.cache {
		if !cached.finished.IsZero() && now.Sub(cached.finished) > cacheLifetime {
			delete(i.cache, key)
		}
	}
	for _, ref := range request.References {
		file, state := i.locate(ref)
		var info os.FileInfo
		if file != nil {
			info = file.info
			file.close()
		}
		if state != "ready" {
			response.Items = append(response.Items, Observation{Reference: ref, State: state, CheckedAt: now})
			continue
		}
		cached := i.cache[ref.Key()]
		if cached != nil && !cached.finished.IsZero() {
			if cached.result.State == "verified" {
				if !sameFile(cached.info, info) {
					delete(i.cache, ref.Key())
					cached = nil
				}
			} else if now.Sub(cached.finished) > failedLifetime {
				delete(i.cache, ref.Key())
				cached = nil
			}
		}
		if cached == nil {
			if len(i.cache) >= maxCacheEntries {
				return response, ErrBusy
			}
			cached = &entry{ref: ref, result: Observation{Reference: ref, State: "pending"}}
			select {
			case i.queue <- cached:
				i.cache[ref.Key()] = cached
			default:
				return response, ErrBusy
			}
		}
		result := cached.result
		result.CheckedAt = now
		if result.State == "pending" {
			response.State = "pending"
		} else if result.State == "verified" {
			result.Cached = true
		}
		response.Items = append(response.Items, result)
	}
	return response, nil
}

type locatedFile struct {
	root      *os.Root
	file      *os.File
	info      os.FileInfo
	directory string
	name      string
}

func (file *locatedFile) close() { file.file.Close(); file.root.Close() }

func sameFile(first, second os.FileInfo) bool {
	return first != nil && second != nil && first.Mode().IsRegular() && second.Mode().IsRegular() && os.SameFile(first, second) &&
		first.Size() == second.Size() && first.ModTime().Equal(second.ModTime())
}

func (i *Inspector) locate(ref Reference) (*locatedFile, string) {
	roots := i.roots[ref.Kind]
	if len(roots) == 0 {
		return nil, "unconfigured"
	}
	var found *locatedFile
	for _, directory := range roots {
		root, err := os.OpenRoot(directory)
		if err != nil {
			if found != nil {
				found.close()
			}
			return nil, "unavailable"
		}
		name := filepath.FromSlash(ref.Name)
		before, err := root.Stat(name)
		if errors.Is(err, os.ErrNotExist) {
			root.Close()
			continue
		}
		if err != nil || !before.Mode().IsRegular() {
			root.Close()
			if found != nil {
				found.close()
			}
			return nil, "unavailable"
		}
		if before.Size() > MaxFileBytes {
			root.Close()
			if found != nil {
				found.close()
			}
			return nil, "too_large"
		}
		file, err := root.Open(name)
		if err != nil {
			root.Close()
			if found != nil {
				found.close()
			}
			return nil, "unavailable"
		}
		after, err := file.Stat()
		if err != nil || !sameFile(before, after) {
			file.Close()
			root.Close()
			if found != nil {
				found.close()
			}
			return nil, "unstable"
		}
		if found != nil {
			file.Close()
			root.Close()
			found.close()
			return nil, "ambiguous"
		}
		found = &locatedFile{root: root, file: file, info: after, directory: directory, name: name}
	}
	if found == nil {
		return nil, "missing"
	}
	return found, "ready"
}

func (i *Inspector) worker() {
	defer i.wg.Done()
	for {
		select {
		case <-i.ctx.Done():
			return
		case item := <-i.queue:
			ctx, cancel := context.WithTimeout(i.ctx, 10*time.Minute)
			result, info := i.hash(ctx, item.ref)
			cancel()
			i.mu.Lock()
			if i.cache[item.ref.Key()] == item {
				item.result = result
				item.info = info
				item.finished = time.Now().UTC()
			}
			i.mu.Unlock()
		}
	}
}

type checkedReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader checkedReader) Read(data []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(data)
}

func (i *Inspector) hash(ctx context.Context, ref Reference) (Observation, os.FileInfo) {
	result := Observation{Reference: ref, State: "unavailable"}
	file, state := i.locate(ref)
	if file == nil {
		result.State = state
		return result, nil
	}
	defer file.close()
	return i.hashOpened(ctx, ref, file, file.file)
}

func (i *Inspector) hashOpened(ctx context.Context, ref Reference, file *locatedFile, reader io.Reader) (Observation, os.FileInfo) {
	result := Observation{Reference: ref, State: "unavailable"}
	digest := sha256.New()
	n, err := io.Copy(digest, checkedReader{ctx, io.LimitReader(reader, file.info.Size()+1)})
	if err != nil {
		return result, nil
	}
	after, err := file.file.Stat()
	if err != nil || n != file.info.Size() || !sameFile(file.info, after) {
		result.State = "unstable"
		return result, nil
	}
	// Resolve configured roots again: an old directory handle must not validate
	// a different file now found through ComfyUI's configured name.
	current, currentState := i.locate(ref)
	if current == nil {
		result.State = "unstable"
		return result, nil
	}
	defer current.close()
	if currentState != "ready" || !sameFile(file.info, current.info) {
		result.State = "unstable"
		return result, nil
	}
	result.State = "verified"
	result.Bytes = n
	result.SHA256 = hex.EncodeToString(digest.Sum(nil))
	result.HashedAt = time.Now().UTC()
	return result, after
}
