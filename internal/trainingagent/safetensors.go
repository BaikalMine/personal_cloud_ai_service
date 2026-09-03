package trainingagent

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxSafeTensorHeaderBytes = 256 << 20

// normalizeSafeTensorPrefix rewrites only the SafeTensors header and streams
// tensor bytes unchanged. This avoids loading a 20+ GB model in RAM.
func normalizeSafeTensorPrefix(sourcePath, targetPath, prefix string) error {
	return normalizeSafeTensorForProfile(sourcePath, targetPath, prefix, "")
}

func normalizeSafeTensorForProfile(sourcePath, targetPath, prefix, family string) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return errors.New("SafeTensors prefix is empty")
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	if targetInfo, statErr := os.Stat(targetPath); statErr == nil && targetInfo.Size() > 8 && !targetInfo.ModTime().Before(sourceInfo.ModTime()) {
		return nil
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	headerLengthBytes := make([]byte, 8)
	if _, err := io.ReadFull(source, headerLengthBytes); err != nil {
		return fmt.Errorf("read SafeTensors header length: %w", err)
	}
	headerLength := binary.LittleEndian.Uint64(headerLengthBytes)
	if headerLength == 0 || headerLength > maxSafeTensorHeaderBytes || int64(headerLength)+8 >= sourceInfo.Size() {
		return errors.New("invalid SafeTensors header length")
	}
	header := make([]byte, int(headerLength))
	if _, err := io.ReadFull(source, header); err != nil {
		return fmt.Errorf("read SafeTensors header: %w", err)
	}
	entries := make(map[string]json.RawMessage)
	if err := json.Unmarshal(header, &entries); err != nil {
		return fmt.Errorf("decode SafeTensors header: %w", err)
	}
	remapped := make(map[string]json.RawMessage, len(entries))
	changed := 0
	for key, value := range entries {
		newKey := key
		if key != "__metadata__" && strings.HasPrefix(key, prefix) {
			newKey = strings.TrimPrefix(key, prefix)
			changed++
		}
		if family == "flux2-klein" && isFlux2ComfyNormWeight(newKey) {
			newKey = strings.TrimSuffix(newKey, ".weight") + ".scale"
			changed++
		}
		if _, duplicate := remapped[newKey]; duplicate {
			return fmt.Errorf("SafeTensors key collision after removing prefix: %s", newKey)
		}
		remapped[newKey] = value
	}
	if changed == 0 {
		return fmt.Errorf("SafeTensors model does not contain prefix %q", prefix)
	}
	encoded, err := json.Marshal(remapped)
	if err != nil {
		return err
	}
	padding := (8 - (len(encoded) % 8)) % 8
	encoded = append(encoded, []byte(strings.Repeat(" ", padding))...)

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		return err
	}
	temporaryPath := targetPath + ".partial"
	_ = os.Remove(temporaryPath)
	target, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	writer := bufio.NewWriterSize(target, 8<<20)
	binary.LittleEndian.PutUint64(headerLengthBytes, uint64(len(encoded)))
	_, writeErr := writer.Write(headerLengthBytes)
	if writeErr == nil {
		_, writeErr = writer.Write(encoded)
	}
	if writeErr == nil {
		_, writeErr = io.CopyBuffer(writer, source, make([]byte, 8<<20))
	}
	if flushErr := writer.Flush(); writeErr == nil {
		writeErr = flushErr
	}
	if syncErr := target.Sync(); writeErr == nil {
		writeErr = syncErr
	}
	if closeErr := target.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(temporaryPath)
		return writeErr
	}
	if err := os.Remove(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

func isFlux2ComfyNormWeight(key string) bool {
	return strings.HasSuffix(key, ".norm.key_norm.weight") || strings.HasSuffix(key, ".norm.query_norm.weight")
}
