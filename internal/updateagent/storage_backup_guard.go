package updateagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"ai-access-gateway/internal/updates"
)

const maxComfyGuardBackupBytes = 64 << 20
const maxComfyGuardBackupRows = 65536

var ErrComfyStorageBackup = errors.New("invalid or inconsistent ComfyUI storage backup")

type comfyGuardBackupWindow struct {
	ID          string `json:"id"`
	RequestHash string `json:"request_hash"`
	ExecutorID  string `json:"executor_id"`
	State       string `json:"state"`
}

type comfyGuardBackupPermit struct {
	ID         string `json:"id"`
	InstanceID string `json:"instance_id"`
	Token      string `json:"token"`
	WorkID     string `json:"work_id,omitempty"`
	WorkLease  string `json:"work_lease,omitempty"`
}

type comfyGuardBackupReceipt struct {
	ID      string                 `json:"id"`
	Payload comfyGuardBackupPermit `json:"payload"`
	Code    string                 `json:"code"`
}

type comfyGuardBackup struct {
	Protocol  int                       `json:"protocol"`
	JournalID string                    `json:"journal_id"`
	Windows   []comfyGuardBackupWindow  `json:"windows"`
	Receipts  []comfyGuardBackupReceipt `json:"receipts"`
}

func validComfyGuardWindowID(id string) bool {
	if len(id) < 16 || len(id) > 128 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

func (snapshot comfyGuardBackup) validate() error {
	if snapshot.Protocol != 1 || !updates.ComfyStorageHex(snapshot.JournalID, 16) || snapshot.Windows == nil || snapshot.Receipts == nil || len(snapshot.Windows) > maxComfyGuardBackupRows || len(snapshot.Receipts) > maxComfyGuardBackupRows {
		return ErrComfyStorageBackup
	}
	held, previous := 0, ""
	for _, item := range snapshot.Windows {
		if !validComfyGuardWindowID(item.ID) || item.ID <= previous || !updates.ComfyStorageHex(item.RequestHash, 32) || !updates.ComfyStorageHex(item.ExecutorID, 32) || item.State != "held" && item.State != "closed" {
			return ErrComfyStorageBackup
		}
		if item.State == "held" {
			held++
		}
		previous = item.ID
	}
	if held > 1 {
		return ErrComfyStorageBackup
	}
	previous = ""
	for _, item := range snapshot.Receipts {
		p := item.Payload
		if !updates.ComfyStorageHex(item.ID, 16) || item.ID <= previous || p.ID != item.ID || !updates.ComfyStorageHex(p.InstanceID, 16) || !updates.ComfyStorageHex(p.Token, 32) || item.Code != "not_started" && item.Code != "quiesced" || (p.WorkID == "") != (p.WorkLease == "") {
			return ErrComfyStorageBackup
		}
		for _, value := range []string{p.WorkID, p.WorkLease} {
			if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 96 || strings.ContainsRune(value, 0) {
				return ErrComfyStorageBackup
			}
		}
		previous = item.ID
	}
	return nil
}

func (g *comfyStorageGuard) backup(ctx context.Context) (comfyGuardBackup, error) {
	var snapshot comfyGuardBackup
	endpoint := *g.endpoint
	endpoint.Path = "/ai-gateway/storage/backup"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return snapshot, ErrComfyStorageGuard
	}
	request.Header.Set("x-ai-gateway-gpu-key", g.token)
	request.Header.Set("Accept", "application/json")
	response, err := g.client.Do(request)
	if err != nil {
		return snapshot, ErrComfyStorageGuard
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return snapshot, ErrComfyStorageGuard
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxComfyGuardBackupBytes+1))
	if err != nil || len(payload) > maxComfyGuardBackupBytes || decodeComfyBackupJSON(payload, &snapshot) != nil || snapshot.validate() != nil {
		return snapshot, ErrComfyStorageBackup
	}
	return snapshot, nil
}

// Reject duplicate fields before typed decoding. Archive JSON must have one
// interpretation in Go and in the standard-library SQLite restore helper.
func decodeComfyBackupJSON(payload []byte, target any) error {
	if !utf8.Valid(payload) {
		return ErrComfyStorageBackup
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return ErrComfyStorageBackup
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delimiter != '{' && delimiter != '[' {
			return ErrComfyStorageBackup
		}
		seen := map[string]bool{}
		for decoder.More() {
			if delimiter == '{' {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || seen[key] {
					return ErrComfyStorageBackup
				}
				seen[key] = true
			}
			if err := value(depth + 1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	}
	if value(0) != nil {
		return ErrComfyStorageBackup
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrComfyStorageBackup
	}
	decoder = json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrComfyStorageBackup
	}
	return nil
}
