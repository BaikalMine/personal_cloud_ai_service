package miningagent

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxRetainedMinerUpdateDirectories = 3

type updateDirectory struct {
	path    string
	name    string
	modTime int64
}

func pruneMinerUpdateArtifacts(currentDir string) error {
	for _, marker := range []string{".backup-", ".failed-"} {
		if err := pruneSiblingUpdateDirectories(currentDir, marker, maxRetainedMinerUpdateDirectories); err != nil {
			return err
		}
	}
	return nil
}

func pruneSiblingUpdateDirectories(currentDir, marker string, keep int) error {
	parent := filepath.Dir(filepath.Clean(currentDir))
	prefix := filepath.Base(filepath.Clean(currentDir)) + marker
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	candidates := make([]updateDirectory, 0)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		candidate := filepath.Join(parent, entry.Name())
		if !pathInsideDirectory(parent, candidate) {
			return errors.New("папка обновления выходит за пределы каталога майнера")
		}
		candidates = append(candidates, updateDirectory{path: candidate, name: entry.Name(), modTime: info.ModTime().UnixNano()})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].modTime == candidates[j].modTime {
			return candidates[i].name > candidates[j].name
		}
		return candidates[i].modTime > candidates[j].modTime
	})
	if keep < 0 {
		keep = 0
	}
	for _, candidate := range candidates[min(keep, len(candidates)):] {
		if err := os.RemoveAll(candidate.path); err != nil {
			return err
		}
	}
	return nil
}

func pathInsideDirectory(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != "" && relative != "." && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
