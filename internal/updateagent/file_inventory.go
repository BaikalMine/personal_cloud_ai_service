package updateagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ai-access-gateway/internal/updates"
)

const maxComfyInventoryEntries = 20000
const maxComfyInventoryDepth = 32

type comfyFileScanner struct {
	kind    string
	issue   string
	visited int
	items   []updates.ComfyFileInventoryItem
}

func (s *comfyFileScanner) fail(issue string) {
	if s.issue == "" {
		s.issue = issue
	}
}

func (s *comfyFileScanner) add(subfolder, filename, kind string, info fs.FileInfo) {
	item := updates.ComfyFileInventoryItem{ID: updates.ComfyFileInventoryID(s.kind, subfolder, filename), Filename: filename, Subfolder: subfolder, EntryType: kind}
	if info != nil {
		modified := info.ModTime().UTC()
		if !modified.IsZero() {
			item.ModifiedAt = &modified
		}
		if kind == "file" && info.Size() >= 0 {
			item.SizeKnown, item.SizeBytes = true, info.Size()
		}
	}
	s.items = append(s.items, item)
}

func (s *comfyFileScanner) walk(ctx context.Context, root *os.Root, subfolder string, depth int) error {
	before, err := root.Lstat(".")
	if err != nil {
		s.fail("scan_error")
		return nil
	}
	directory, err := root.Open(".")
	if err != nil {
		s.fail("scan_error")
		return nil
	}
	defer directory.Close()
	opened, err := directory.Stat()
	if err != nil || !os.SameFile(before, opened) {
		s.fail("scan_changed")
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := maxComfyInventoryEntries - s.visited
		entries, readErr := directory.ReadDir(min(256, remaining+1))
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if s.visited == maxComfyInventoryEntries {
				s.fail("scan_limit")
				return nil
			}
			s.visited++
			name := entry.Name()
			if !updates.ValidComfyInventoryName(name, subfolder) {
				s.fail("scan_error")
				continue
			}
			info, err := root.Lstat(name)
			if err != nil {
				s.add(subfolder, name, "unavailable", nil)
				s.fail("scan_changed")
				continue
			}
			if comfyInventoryLink(info) {
				s.add(subfolder, name, "link", info)
				continue
			}
			if info.IsDir() {
				if depth >= maxComfyInventoryDepth {
					s.add(subfolder, name, "unavailable_directory", info)
					s.fail("scan_limit")
					continue
				}
				child, err := root.OpenRoot(name)
				if err != nil {
					s.add(subfolder, name, "unavailable_directory", info)
					s.fail("scan_error")
					continue
				}
				opened, openErr := child.Lstat(".")
				if openErr != nil || !os.SameFile(info, opened) {
					child.Close()
					s.add(subfolder, name, "unavailable_directory", info)
					s.fail("scan_changed")
					continue
				}
				err = s.walk(ctx, child, path.Join(subfolder, name), depth+1)
				child.Close()
				current, statErr := root.Lstat(name)
				if statErr != nil || !os.SameFile(info, current) {
					s.fail("scan_changed")
				}
				if err != nil {
					return err
				}
			} else if info.Mode().IsRegular() && info.Size() >= 0 {
				s.add(subfolder, name, "file", info)
			} else {
				s.add(subfolder, name, "other", info)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			s.fail("scan_error")
			break
		}
	}
	after, err := root.Lstat(".")
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		s.fail("scan_changed")
	}
	return nil
}

// This is metadata observation, not a content fingerprint or deletion preview.
// User input never supplies a filesystem root or a path to open.
func ComfyFileInventory(ctx context.Context, target ComfyTarget, query updates.ComfyFileInventoryQuery) (updates.ComfyFileInventory, error) {
	result := updates.ComfyFileInventory{Protocol: 1, Kind: query.Kind, CheckedAt: time.Now().UTC(), Items: []updates.ComfyFileInventoryItem{}}
	if err := query.Validate(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	directory := target.OutputDirectory
	if query.Kind == "input" {
		directory = target.InputDirectory
	}
	if directory == "" && filepath.IsAbs(target.WorkingDirectory) {
		directory = filepath.Join(target.WorkingDirectory, query.Kind)
	}
	if filepath.IsAbs(directory) {
		directory = filepath.Clean(directory)
	}
	scanner := comfyFileScanner{kind: query.Kind, items: []updates.ComfyFileInventoryItem{}}
	rootIdentity := ""
	if !filepath.IsAbs(directory) {
		scanner.fail("root_rejected")
	} else if info, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		scanner.fail("root_missing")
	} else if err != nil {
		scanner.fail("root_unavailable")
	} else if !info.IsDir() || comfyInventoryLink(info) {
		scanner.fail("root_rejected")
	} else {
		rootIdentity, err = storageRootIdentity(directory)
		if err != nil {
			scanner.fail("root_unavailable")
		} else {
			root, err := os.OpenRoot(directory)
			if err != nil {
				scanner.fail("root_unavailable")
			} else {
				opened, statErr := root.Lstat(".")
				if statErr != nil || !os.SameFile(info, opened) {
					scanner.fail("scan_changed")
				} else {
					err = scanner.walk(ctx, root, "", 0)
				}
				root.Close()
				if err != nil {
					return result, err
				}
			}
			current, err := storageRootIdentity(directory)
			if err != nil || current != rootIdentity {
				scanner.fail("scan_changed")
			}
		}
	}
	sort.Slice(scanner.items, func(i, j int) bool {
		a, b := scanner.items[i], scanner.items[j]
		return a.Subfolder+"/"+a.Filename < b.Subfolder+"/"+b.Filename
	})
	result.TotalItems = len(scanner.items)
	matched := []updates.ComfyFileInventoryItem{}
	for _, item := range scanner.items {
		if item.SizeKnown {
			if result.LogicalBytes > math.MaxInt64-item.SizeBytes {
				return result, errors.New("ComfyUI inventory size overflow")
			}
			result.TotalFiles++
			result.LogicalBytes += item.SizeBytes
		}
		if query.Search == "" || strings.Contains(strings.ToLower(item.Subfolder+"/"+item.Filename), strings.ToLower(query.Search)) {
			matched = append(matched, item)
		}
	}
	result.Issue, result.Complete, result.MatchedItems = scanner.issue, scanner.issue == "", len(matched)
	encoded, err := json.Marshal(struct {
		Root, Kind, Issue, Search string
		Items                     []updates.ComfyFileInventoryItem
	}{rootIdentity, query.Kind, result.Issue, strings.ToLower(query.Search), scanner.items})
	if err != nil {
		return updates.ComfyFileInventory{}, errors.New("ComfyUI inventory metadata cannot be encoded")
	}
	digest := sha256.Sum256(encoded)
	result.Snapshot = hex.EncodeToString(digest[:])
	if query.Snapshot != "" && query.Snapshot != result.Snapshot {
		return updates.ComfyFileInventory{}, updates.ErrComfyInventoryChanged
	}
	start := 0
	if query.Cursor != "" {
		found := false
		for i, item := range matched {
			if item.ID == query.Cursor {
				start, found = i+1, true
				break
			}
		}
		if !found {
			return updates.ComfyFileInventory{}, updates.ErrComfyInventoryChanged
		}
	}
	end := min(start+query.Limit, len(matched))
	result.Items = append(result.Items, matched[start:end]...)
	if end < len(matched) {
		result.NextCursor = matched[end-1].ID
	}
	return result, ctx.Err()
}

func (s *Server) handleComfyFileInventory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	query, err := updates.ParseComfyFileInventoryQuery(r.URL.RawQuery)
	if err != nil {
		writeComfyStorageError(w, http.StatusBadRequest, "invalid_inventory_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	result, err := ComfyFileInventory(ctx, s.comfyTarget, query)
	if errors.Is(err, updates.ErrComfyInventoryChanged) {
		writeComfyStorageError(w, http.StatusConflict, "inventory_changed")
		return
	}
	if err != nil {
		writeComfyStorageError(w, http.StatusServiceUnavailable, "inventory_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
