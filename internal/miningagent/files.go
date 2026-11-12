package miningagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"ai-access-gateway/internal/mining"
)

const maxEditableScriptBytes = 64 << 10

// Optional capability keeps older and non-Windows controllers compatible.
type fileController interface {
	Files(context.Context) (mining.FileCatalog, error)
	SaveScript(context.Context, mining.SaveScriptRequest) (mining.Script, error)
}

func launchFiles(ctx context.Context, root string) (mining.FileCatalog, error) {
	result := mining.FileCatalog{Root: root, Files: []mining.LaunchFile{}}
	visited := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		visited++
		if visited > 20000 || len(result.Files) >= 1000 {
			return errors.New("слишком много файлов в папке майнинга")
		}
		resolved, err := containedFile(root, path)
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(resolved))
		if ext != ".bat" && ext != ".cmd" && ext != ".exe" {
			return nil
		}
		rel, _ := filepath.Rel(root, resolved)
		kind := "script"
		if ext == ".exe" {
			kind = "executable"
		}
		result.Files = append(result.Files, mining.LaunchFile{Path: resolved, Name: filepath.Base(resolved), Folder: filepath.Dir(rel), Kind: kind})
		return nil
	})
	sort.Slice(result.Files, func(i, j int) bool {
		return strings.ToLower(result.Files[i].Path) < strings.ToLower(result.Files[j].Path)
	})
	return result, err
}

func containedFile(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("требуется полный путь к файлу")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("файл находится вне разрешённой папки майнинга")
	}
	return resolved, nil
}

func editableScript(root, path string) (string, []byte, mining.Script, error) {
	resolved, err := containedFile(root, path)
	if err != nil {
		return "", nil, mining.Script{}, err
	}
	ext := strings.ToLower(filepath.Ext(resolved))
	if ext != ".bat" && ext != ".cmd" {
		return "", nil, mining.Script{}, errors.New("редактировать можно только BAT и CMD")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxEditableScriptBytes {
		return "", nil, mining.Script{}, errors.New("BAT не найден или больше 64 КБ")
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", nil, mining.Script{}, err
	}
	content, _, err := scriptEncoding(data)
	if err != nil {
		return "", nil, mining.Script{}, err
	}
	return resolved, data, mining.Script{Path: resolved, Content: content, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}, nil
}

func scriptEncoding(data []byte) (string, func(string) []byte, error) {
	if len(data) >= 2 && (bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff})) {
		if len(data)%2 != 0 {
			return "", nil, errors.New("повреждённая UTF-16 кодировка")
		}
		var order binary.ByteOrder = binary.LittleEndian
		if data[0] == 0xfe {
			order = binary.BigEndian
		}
		units := make([]uint16, (len(data)-2)/2)
		for i := range units {
			units[i] = order.Uint16(data[2+i*2:])
		}
		encode := func(s string) []byte {
			u := utf16.Encode([]rune(s))
			out := make([]byte, 2+2*len(u))
			copy(out, data[:2])
			for i, v := range u {
				order.PutUint16(out[2+i*2:], v)
			}
			return out
		}
		content := string(utf16.Decode(units))
		if !bytes.Equal(encode(content), data) {
			return "", nil, errors.New("повреждённая UTF-16 кодировка")
		}
		return content, encode, nil
	}
	bom := bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf})
	body := data
	if bom {
		body = data[3:]
	}
	if !utf8.Valid(body) {
		return "", nil, errors.New("кодировка BAT не UTF-8/UTF-16: сохранение отключено, чтобы не повредить файл")
	}
	return string(body), func(s string) []byte {
		if bom {
			return append([]byte{0xef, 0xbb, 0xbf}, []byte(s)...)
		}
		return []byte(s)
	}, nil
}

func saveEditableScript(ctx context.Context, root string, input mining.SaveScriptRequest) (mining.Script, error) {
	if err := ctx.Err(); err != nil {
		return mining.Script{}, err
	}
	path, data, current, err := editableScript(root, input.ScriptPath)
	if err != nil {
		return current, err
	}
	if !validSHA256(input.ExpectedSHA256) || current.SHA256 != input.ExpectedSHA256 {
		return mining.Script{}, errors.New("BAT изменился после открытия. Откройте файл заново перед сохранением")
	}
	if !utf8.ValidString(input.Content) || strings.ContainsRune(input.Content, 0) || len(input.Content) > maxEditableScriptBytes {
		return mining.Script{}, errors.New("некорректный текст или размер BAT больше 64 КБ")
	}
	_, encode, _ := scriptEncoding(data)
	// Browser textareas normalize newlines; restore the original line-ending style.
	content := strings.ReplaceAll(input.Content, "\r\n", "\n")
	if bytes.Contains(data, []byte("\r\n")) || strings.Contains(current.Content, "\r\n") {
		content = strings.ReplaceAll(content, "\n", "\r\n")
	}
	updated := encode(content)
	if len(updated) > maxEditableScriptBytes {
		return mining.Script{}, errors.New("размер BAT больше 64 КБ")
	}
	if bytes.Equal(data, updated) {
		return current, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return mining.Script{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".miner-edit-*.tmp")
	if err != nil {
		return mining.Script{}, err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(info.Mode().Perm()); err == nil {
		_, err = tmp.Write(updated)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return mining.Script{}, err
	}
	latest, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(latest, data) {
		return mining.Script{}, errors.New("BAT изменился во время сохранения. Откройте его заново")
	}
	backup := path + "." + time.Now().UTC().Format("20060102T150405.000000000Z") + ".bak"
	f, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return mining.Script{}, err
	}
	_, err = f.Write(data)
	closeErr = f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return mining.Script{}, err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return mining.Script{}, err
	}
	current.Content, current.SHA256 = content, fmt.Sprintf("%x", sha256.Sum256(updated))
	return current, nil
}
