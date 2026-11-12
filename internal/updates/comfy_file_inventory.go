package updates

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrComfyInventoryRequest = errors.New("invalid ComfyUI file inventory request")
var ErrComfyInventoryChanged = errors.New("ComfyUI file inventory changed")

type ComfyFileInventoryQuery struct {
	Kind     string
	Search   string
	Cursor   string
	Snapshot string
	Limit    int
}

type ComfyFileInventoryItem struct {
	ID         string     `json:"id"`
	Filename   string     `json:"filename"`
	Subfolder  string     `json:"subfolder"`
	EntryType  string     `json:"entry_type"`
	SizeBytes  int64      `json:"size_bytes"`
	SizeKnown  bool       `json:"size_known"`
	ModifiedAt *time.Time `json:"modified_at,omitempty"`
}

type ComfyFileInventory struct {
	Protocol     int                      `json:"protocol"`
	Kind         string                   `json:"kind"`
	CheckedAt    time.Time                `json:"checked_at"`
	Complete     bool                     `json:"complete"`
	Issue        string                   `json:"issue"`
	Snapshot     string                   `json:"snapshot"`
	TotalItems   int                      `json:"total_items"`
	TotalFiles   int                      `json:"total_files"`
	LogicalBytes int64                    `json:"logical_bytes"`
	MatchedItems int                      `json:"matched_items"`
	NextCursor   string                   `json:"next_cursor"`
	Items        []ComfyFileInventoryItem `json:"items"`
}

func ComfyFileInventoryID(kind, subfolder, filename string) string {
	digest := sha256.Sum256([]byte("comfy-file-observation/v1\x00" + kind + "\x00" + subfolder + "\x00" + filename))
	return hex.EncodeToString(digest[:])
}

func (q ComfyFileInventoryQuery) Validate() error {
	if q.Kind != "input" && q.Kind != "output" || q.Limit < 1 || q.Limit > 100 || len(q.Search) > 200 || !utf8.ValidString(q.Search) || strings.ContainsRune(q.Search, 0) ||
		(q.Cursor == "") != (q.Snapshot == "") || q.Cursor != "" && (!ComfyStorageHex(q.Cursor, 32) || !ComfyStorageHex(q.Snapshot, 32)) {
		return ErrComfyInventoryRequest
	}
	return nil
}

func ParseComfyFileInventoryQuery(raw string) (ComfyFileInventoryQuery, error) {
	q := ComfyFileInventoryQuery{Kind: "output", Limit: 50}
	values, err := url.ParseQuery(raw)
	if err != nil || len(raw) > 2048 {
		return q, ErrComfyInventoryRequest
	}
	for key, values := range values {
		if len(values) != 1 {
			return q, ErrComfyInventoryRequest
		}
		value := values[0]
		switch key {
		case "kind":
			q.Kind = value
		case "q":
			q.Search = value
		case "cursor":
			q.Cursor = value
		case "snapshot":
			q.Snapshot = value
		case "limit":
			q.Limit, err = strconv.Atoi(value)
			if err != nil || strconv.Itoa(q.Limit) != value {
				return q, ErrComfyInventoryRequest
			}
		default:
			return q, ErrComfyInventoryRequest
		}
	}
	return q, q.Validate()
}

func (q ComfyFileInventoryQuery) Values() url.Values {
	v := url.Values{"kind": {q.Kind}, "limit": {strconv.Itoa(q.Limit)}}
	if q.Search != "" {
		v.Set("q", q.Search)
	}
	if q.Cursor != "" {
		v.Set("cursor", q.Cursor)
		v.Set("snapshot", q.Snapshot)
	}
	return v
}

func ValidComfyInventoryName(filename, subfolder string) bool {
	if filename == "" || filename == "." || filename == ".." || len(filename) > 1024 || len(subfolder) > 4096 ||
		!utf8.ValidString(filename) || !utf8.ValidString(subfolder) || strings.ContainsAny(filename, "/\\:\x00") || strings.ContainsAny(subfolder, "\\:\x00") || strings.HasPrefix(subfolder, "/") {
		return false
	}
	if subfolder != "" {
		for _, part := range strings.Split(subfolder, "/") {
			if part == "" || part == "." || part == ".." {
				return false
			}
		}
	}
	return true
}

func ValidateComfyFileInventory(result ComfyFileInventory, q ComfyFileInventoryQuery) error {
	if q.Validate() != nil || result.Protocol != 1 || result.Kind != q.Kind || result.CheckedAt.IsZero() || result.Items == nil || len(result.Items) > q.Limit ||
		!ComfyStorageHex(result.Snapshot, 32) || result.TotalItems < 0 || result.TotalItems > 20000 || result.TotalFiles < 0 || result.TotalFiles > result.TotalItems || result.LogicalBytes < 0 ||
		result.MatchedItems < len(result.Items) || result.MatchedItems > result.TotalItems || result.Complete != (result.Issue == "") {
		return ErrComfyInventoryRequest
	}
	switch result.Issue {
	case "", "root_missing", "root_unavailable", "root_rejected", "scan_limit", "scan_error", "scan_changed":
	default:
		return ErrComfyInventoryRequest
	}
	seen := map[string]bool{}
	previous := ""
	pageFiles, pageBytes := 0, int64(0)
	for _, item := range result.Items {
		path := item.Subfolder + "/" + item.Filename
		if !ValidComfyInventoryName(item.Filename, item.Subfolder) || item.ID != ComfyFileInventoryID(q.Kind, item.Subfolder, item.Filename) || seen[item.ID] || item.ID == q.Cursor ||
			path <= previous || item.SizeBytes < 0 || (!item.SizeKnown && item.SizeBytes != 0) || item.SizeKnown != (item.EntryType == "file") ||
			item.ModifiedAt != nil && item.ModifiedAt.IsZero() || q.Search != "" && !strings.Contains(strings.ToLower(item.Subfolder+"/"+item.Filename), strings.ToLower(q.Search)) {
			return ErrComfyInventoryRequest
		}
		switch item.EntryType {
		case "file", "link", "other", "unavailable_directory", "unavailable":
		default:
			return ErrComfyInventoryRequest
		}
		seen[item.ID], previous = true, path
		if item.SizeKnown {
			pageFiles++
			if item.SizeBytes > result.LogicalBytes-pageBytes {
				return ErrComfyInventoryRequest
			}
			pageBytes += item.SizeBytes
		}
	}
	if pageFiles > result.TotalFiles {
		return ErrComfyInventoryRequest
	}
	if q.Cursor == "" && (len(result.Items) != min(q.Limit, result.MatchedItems) || (result.NextCursor != "") != (result.MatchedItems > len(result.Items))) {
		return ErrComfyInventoryRequest
	}
	if result.NextCursor != "" && (len(result.Items) != q.Limit || result.NextCursor != result.Items[len(result.Items)-1].ID || result.NextCursor == q.Cursor) {
		return ErrComfyInventoryRequest
	}
	if q.Snapshot != "" && q.Snapshot != result.Snapshot {
		return ErrComfyInventoryChanged
	}
	return nil
}
