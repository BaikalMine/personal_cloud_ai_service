package store

import (
	"context"
	"encoding/json"

	"ai-access-gateway/internal/updates"
)

// A matching path is not evidence that current bytes belong to the registry.
// No ownership, deletion authorization or quota mutation is inferred here.
func (s *Store) ComfyFileRegistrations(ctx context.Context, kind string, items []updates.ComfyFileInventoryItem) (map[string]bool, error) {
	registered := make(map[string]bool, len(items))
	if kind != "input" && kind != "output" || len(items) > 100 {
		return nil, ErrComfyStorageQuery
	}
	type entry struct {
		ID        string `json:"id"`
		Filename  string `json:"filename"`
		Subfolder string `json:"subfolder"`
	}
	entries := make([]entry, 0, len(items))
	for _, item := range items {
		if !updates.ValidComfyInventoryName(item.Filename, item.Subfolder) || item.ID != updates.ComfyFileInventoryID(kind, item.Subfolder, item.Filename) {
			return nil, ErrComfyStorageQuery
		}
		if _, found := registered[item.ID]; found {
			return nil, ErrComfyStorageQuery
		}
		registered[item.ID] = false
		entries = append(entries, entry{item.ID, item.Filename, item.Subfolder})
	}
	if len(entries) == 0 {
		return registered, nil
	}
	payload, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `WITH selected AS (
 SELECT id,lower(filename) filename,lower(subfolder) subfolder FROM jsonb_to_recordset($1::jsonb) AS x(id text,filename text,subfolder text)
), paths AS (
 SELECT s.id FROM selected s JOIN comfy_input_assets a ON lower(a.filename)=s.filename AND lower(replace(a.subfolder,chr(92),'/'))=s.subfolder WHERE $2='input'
 UNION
 SELECT s.id FROM selected s JOIN comfy_output_ownership o ON lower(o.filename)=s.filename AND lower(replace(o.subfolder,chr(92),'/'))=s.subfolder WHERE $2='output' AND o.storage_type='output'
 UNION
 SELECT s.id FROM selected s JOIN comfy_output_cleanup_tombstones t ON lower(t.filename)=s.filename AND lower(replace(t.subfolder,chr(92),'/'))=s.subfolder WHERE $2='output' AND t.storage_type='output'
 UNION
 SELECT s.id FROM selected s JOIN content_media m ON lower(m.original_name)=s.filename AND lower(replace(m.subfolder,chr(92),'/'))=s.subfolder
 JOIN content_events e ON e.id=m.event_id WHERE $2='output' AND m.storage_type='output' AND e.service='comfyui'
) SELECT id FROM paths`, string(payload), kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		registered[id] = true
	}
	return registered, rows.Err()
}
