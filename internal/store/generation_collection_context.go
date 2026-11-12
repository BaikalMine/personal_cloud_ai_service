package store

import (
	"context"
	"database/sql"
	"errors"

	"ai-access-gateway/internal/domain"
	"github.com/lib/pq"
)

var (
	ErrGenerationCollectionConflict = errors.New("collection revision conflict")
	ErrGenerationCollectionName     = errors.New("collection name already exists")
	ErrGenerationCollectionMissing  = errors.New("collection is not available")
	ErrGenerationCollectionInvalid  = errors.New("invalid collection context")
)

const generationCollectionContextColumns = `id,user_id,name,revision,context_cipher,context_saved_at,created_at,updated_at`

func scanGenerationCollectionContext(scanner interface{ Scan(...any) error }) (domain.GenerationCollectionContext, error) {
	var row domain.GenerationCollectionContext
	err := scanner.Scan(&row.Collection.ID, &row.UserID, &row.Collection.Name, &row.Collection.Revision,
		&row.PayloadCipher, &row.ContextSavedAt, &row.Collection.CreatedAt, &row.Collection.UpdatedAt)
	return row, err
}

func (s *Store) GenerationCollectionContext(ctx context.Context, userID, collectionID int64) (domain.GenerationCollectionContext, error) {
	return scanGenerationCollectionContext(s.db.QueryRowContext(ctx, `SELECT `+generationCollectionContextColumns+`
		FROM generation_media_collections WHERE id=$1 AND user_id=$2`, collectionID, userID))
}

func (s *Store) SaveGenerationCollectionContext(ctx context.Context, params domain.SaveGenerationCollectionContextParams) (domain.GenerationCollectionContext, error) {
	name, key, err := normalizeGenerationLibraryText(params.Name, 80)
	if err != nil {
		return domain.GenerationCollectionContext{}, errors.Join(ErrGenerationCollectionInvalid, err)
	}
	if params.UserID <= 0 || params.CollectionID <= 0 || params.ExpectedRevision <= 0 || params.ExpectedDraftRevision < 0 ||
		params.ReplaceContext && (len(params.PayloadCipher) == 0 || len(params.PayloadCipher) > (1<<20)+64) {
		return domain.GenerationCollectionContext{}, ErrGenerationCollectionInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationCollectionContext{}, err
	}
	defer tx.Rollback()
	current, err := scanGenerationCollectionContext(tx.QueryRowContext(ctx, `SELECT `+generationCollectionContextColumns+`
		FROM generation_media_collections WHERE id=$1 AND user_id=$2 FOR UPDATE`, params.CollectionID, params.UserID))
	if err != nil {
		return domain.GenerationCollectionContext{}, err
	}
	if current.Collection.Revision != params.ExpectedRevision {
		return domain.GenerationCollectionContext{}, ErrGenerationCollectionConflict
	}
	// A saved draft must still be the exact source the caller inspected.
	if params.ExpectedDraftRevision > 0 {
		var revision int64
		err = tx.QueryRowContext(ctx, `SELECT revision FROM generation_drafts
			WHERE user_id=$1 AND revision=$2 AND expires_at>now() FOR SHARE`, params.UserID, params.ExpectedDraftRevision).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.GenerationCollectionContext{}, ErrGenerationDraftConflict
		}
		if err != nil {
			return domain.GenerationCollectionContext{}, err
		}
	}
	row, err := scanGenerationCollectionContext(tx.QueryRowContext(ctx, `UPDATE generation_media_collections
		SET name=$3,name_key=$4,revision=nextval('generation_collection_revision_seq'),updated_at=now(),
		    context_cipher=CASE WHEN $5 THEN $6 ELSE context_cipher END,
		    context_saved_at=CASE WHEN $5 THEN now() ELSE context_saved_at END
		WHERE id=$1 AND user_id=$2 RETURNING `+generationCollectionContextColumns,
		params.CollectionID, params.UserID, name, key, params.ReplaceContext, params.PayloadCipher))
	if err != nil {
		var pgError *pq.Error
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return domain.GenerationCollectionContext{}, ErrGenerationCollectionName
		}
		return domain.GenerationCollectionContext{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationCollectionContext{}, err
	}
	return row, nil
}

func bindGenerationCollectionJob(ctx context.Context, tx *sql.Tx, userID, collectionID, jobID int64) error {
	if collectionID == 0 {
		return nil
	}
	if collectionID < 0 || userID <= 0 || jobID <= 0 {
		return ErrGenerationCollectionMissing
	}
	var ownedID int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM generation_media_collections
		WHERE id=$1 AND user_id=$2 FOR KEY SHARE`, collectionID, userID).Scan(&ownedID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGenerationCollectionMissing
	}
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO generation_collection_jobs(job_id,collection_id)
		SELECT id,$2 FROM generation_jobs WHERE id=$1 AND user_id=$3`, jobID, collectionID, userID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return ErrGenerationCollectionMissing
	}
	return err
}

// Opening a collection cannot replace a newer collection snapshot or draft.
// The gateway prepares the encrypted draft from this exact collection revision.
func (s *Store) OpenGenerationCollectionDraft(ctx context.Context, userID, collectionID, collectionRevision, draftRevision int64, cipher []byte) (domain.GenerationDraftRow, error) {
	if userID <= 0 || collectionID <= 0 || collectionRevision <= 0 || draftRevision < 0 || len(cipher) == 0 || len(cipher) > (1<<20)+64 {
		return domain.GenerationDraftRow{}, errors.New("invalid collection draft")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationDraftRow{}, err
	}
	defer tx.Rollback()
	current, err := scanGenerationCollectionContext(tx.QueryRowContext(ctx, `SELECT `+generationCollectionContextColumns+`
		FROM generation_media_collections WHERE id=$1 AND user_id=$2 FOR SHARE`, collectionID, userID))
	if err != nil {
		return domain.GenerationDraftRow{}, err
	}
	if current.Collection.Revision != collectionRevision {
		return domain.GenerationDraftRow{}, ErrGenerationCollectionConflict
	}
	if len(current.PayloadCipher) == 0 {
		return domain.GenerationDraftRow{}, ErrGenerationCollectionMissing
	}
	row, err := saveGenerationDraft(ctx, tx, userID, draftRevision, cipher)
	if err != nil {
		return domain.GenerationDraftRow{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationDraftRow{}, err
	}
	return row, nil
}
