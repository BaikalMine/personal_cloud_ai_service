package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"ai-access-gateway/internal/domain"
	"github.com/lib/pq"
)

var (
	ErrInviteTemplateConflict = errors.New("invitation template changed or was deleted")
	ErrInviteTemplateName     = errors.New("invitation template name is already used")
	ErrInviteTemplateDenied   = errors.New("invitation template requires an active administrator")
)

func decodeInviteTemplate(raw []byte) (domain.InviteTemplateSettings, error) {
	var settings domain.InviteTemplateSettings
	if len(raw) > 8192 {
		return settings, fmt.Errorf("invitation template is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, err
	}
	var tail any
	if err := decoder.Decode(&tail); err != io.EOF {
		return settings, fmt.Errorf("invalid invitation template tail")
	}
	return settings, settings.Validate()
}

func lockInviteTemplateAdmin(ctx context.Context, tx *sql.Tx, actorID int64) error {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND role='admin' AND NOT disabled
	 AND (account_expires_at IS NULL OR account_expires_at>clock_timestamp()) FOR SHARE`, actorID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInviteTemplateDenied
	}
	return err
}

// SaveInviteTemplate uses the displayed revision for compare-and-swap. Existing
// invites contain their own settings and deliberately have no template FK.
func (s *Store) SaveInviteTemplate(ctx context.Context, actorID int64, item domain.InviteTemplate) (domain.InviteTemplate, error) {
	item.Name = strings.TrimSpace(item.Name)
	if !utf8.ValidString(item.Name) || utf8.RuneCountInString(item.Name) < 1 || utf8.RuneCountInString(item.Name) > 80 ||
		strings.IndexFunc(item.Name, unicode.IsControl) >= 0 || item.ID < 0 ||
		(item.ID == 0 && item.Revision != 0) || (item.ID > 0 && item.Revision < 1) {
		return domain.InviteTemplate{}, fmt.Errorf("invalid invitation template identity or name")
	}
	if err := item.Settings.Validate(); err != nil {
		return domain.InviteTemplate{}, err
	}
	raw, err := json.Marshal(item.Settings)
	if err != nil {
		return domain.InviteTemplate{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.InviteTemplate{}, err
	}
	defer tx.Rollback()
	if err := lockInviteTemplateAdmin(ctx, tx, actorID); err != nil {
		return domain.InviteTemplate{}, err
	}
	if item.ID == 0 {
		err = tx.QueryRowContext(ctx, `INSERT INTO invite_templates(name,settings,created_by_user_id,updated_by_user_id)
		 VALUES($1,$2,$3,$3) RETURNING id,revision,created_at,updated_at`, item.Name, string(raw), actorID).
			Scan(&item.ID, &item.Revision, &item.CreatedAt, &item.UpdatedAt)
	} else {
		err = tx.QueryRowContext(ctx, `UPDATE invite_templates SET name=$3, settings=$4,
		 updated_by_user_id=$5, updated_at=clock_timestamp(), revision=revision+1 WHERE id=$1 AND revision=$2
		 RETURNING revision,created_at,updated_at`, item.ID, item.Revision, item.Name, string(raw), actorID).
			Scan(&item.Revision, &item.CreatedAt, &item.UpdatedAt)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.InviteTemplate{}, ErrInviteTemplateConflict
	}
	var pg *pq.Error
	if errors.As(err, &pg) && pg.Code == "23505" && pg.Constraint == "invite_templates_name_idx" {
		return domain.InviteTemplate{}, ErrInviteTemplateName
	}
	if err != nil {
		return domain.InviteTemplate{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.InviteTemplate{}, err
	}
	return item, nil
}

func (s *Store) DeleteInviteTemplate(ctx context.Context, actorID, id, revision int64) error {
	if id <= 0 || revision <= 0 {
		return ErrInviteTemplateConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockInviteTemplateAdmin(ctx, tx, actorID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM invite_templates WHERE id=$1 AND revision=$2`, id, revision)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrInviteTemplateConflict
	}
	return tx.Commit()
}

func (s *Store) InviteTemplate(ctx context.Context, actorID, id int64) (domain.InviteTemplate, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.InviteTemplate{}, err
	}
	defer tx.Rollback()
	if err := lockInviteTemplateAdmin(ctx, tx, actorID); err != nil {
		return domain.InviteTemplate{}, err
	}
	var item domain.InviteTemplate
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT id,name,revision,settings,created_at,updated_at FROM invite_templates WHERE id=$1`, id).
		Scan(&item.ID, &item.Name, &item.Revision, &raw, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return domain.InviteTemplate{}, err
	}
	item.Settings, err = decodeInviteTemplate(raw)
	if err != nil {
		return domain.InviteTemplate{}, err
	}
	return item, tx.Commit()
}

type InviteTemplatePage struct {
	Items  []domain.InviteTemplate
	Total  int
	Offset int
}

func (s *Store) ListInviteTemplates(ctx context.Context, actorID int64, search string, offset int) (InviteTemplatePage, error) {
	page := InviteTemplatePage{Items: []domain.InviteTemplate{}}
	if offset < 0 || offset > 1000000 || !utf8.ValidString(search) || utf8.RuneCountInString(search) > 80 {
		return page, fmt.Errorf("invalid invitation template query")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err := lockInviteTemplateAdmin(ctx, tx, actorID); err != nil {
		return page, err
	}
	search = strings.TrimSpace(search)
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM invite_templates WHERE strpos(lower(name),lower($1))>0`, search).Scan(&page.Total); err != nil {
		return page, err
	}
	if offset >= page.Total && page.Total > 0 {
		offset = ((page.Total - 1) / 50) * 50
	}
	if page.Total == 0 {
		offset = 0
	}
	page.Offset = offset
	rows, err := tx.QueryContext(ctx, `SELECT id,name,revision,settings,created_at,updated_at FROM invite_templates
	 WHERE strpos(lower(name),lower($1))>0 ORDER BY lower(name),id LIMIT 50 OFFSET $2`, search, offset)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.InviteTemplate
		var raw []byte
		if err := rows.Scan(&item.ID, &item.Name, &item.Revision, &raw, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return page, err
		}
		item.Settings, err = decodeInviteTemplate(raw)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if err := rows.Close(); err != nil {
		return page, err
	}
	return page, tx.Commit()
}

func (p *CreateInviteParams) ApplyAccess(s domain.AccessSettings) {
	p.GrantComfyUI, p.GrantOpenWebUI, p.GrantQuickGeneration = s.ComfyUI, s.OpenWebUI, s.QuickGeneration
	p.GrantTextToImage, p.GrantImageToImage, p.GrantVideo = s.TextToImage, s.ImageToImage, s.Video
	p.GrantAdvancedGenerationSettings, p.GrantTrainImageLora = s.AdvancedSettings, s.Training
	p.GrantHarness = s.Harness
	p.PauseMiningForQuickGeneration, p.QueuePriority = s.PauseMining, s.QueuePriority
	p.GenerationDailyLimit, p.GenerationTotalLimit = s.ImageDailyLimit, s.ImageTotalLimit
	p.VideoGenerationDailyLimit, p.VideoGenerationTotalLimit = s.VideoDailyLimit, s.VideoTotalLimit
	p.MaxVideoGenerationQuality = s.MaxVideoQuality
}

func (p *SetServiceAccessParams) ApplyAccess(s domain.AccessSettings) {
	p.ComfyUI, p.OpenWebUI, p.QuickGeneration = s.ComfyUI, s.OpenWebUI, s.QuickGeneration
	p.TextToImage, p.ImageToImage, p.Video = s.TextToImage, s.ImageToImage, s.Video
	p.AdvancedGenerationSettings, p.TrainImageLora = s.AdvancedSettings, s.Training
	p.Harness = s.Harness
	p.PauseMiningForQuickGeneration, p.QueuePriority = s.PauseMining, s.QueuePriority
	p.ImageDailyLimit, p.ImageTotalLimit = s.ImageDailyLimit, s.ImageTotalLimit
	p.VideoDailyLimit, p.VideoTotalLimit = s.VideoDailyLimit, s.VideoTotalLimit
	p.MaxVideoQuality = s.MaxVideoQuality
}
