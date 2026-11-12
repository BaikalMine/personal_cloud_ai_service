package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"ai-access-gateway/internal/domain"
)

var ErrLoraVerificationSource = errors.New("LoRA verification source changed or unavailable")

func (s *Store) LoraVerificationBatchByRequest(ctx context.Context, userID int64, requestID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT b.public_id FROM generation_batches b JOIN lora_verifications v ON v.batch_id=b.id
 WHERE b.user_id=$1 AND v.user_id=$1 AND b.request_id=$2 AND b.experiment_mode='lora_verification'`, userID, requestID).Scan(&id)
	return id, err
}

func validateLoraVerificationBatch(params domain.CreateGenerationBatchParams) error {
	verification := params.LoraVerification
	if params.Mode != domain.GenerationBatchLoraVerification {
		if verification != nil {
			return domain.ErrLoraVerification
		}
		return nil
	}
	if verification == nil || verification.Validate() != nil || params.ParentBatchID != nil || params.SourceJobID != nil || params.ParameterName != "" || len(params.ParameterValues) != 0 || len(verification.Members) != len(params.Jobs) || params.MaxParallel != 1 {
		return domain.ErrLoraVerification
	}
	if params.TemplateID != "text-to-image" && params.TemplateID != "image-to-image" {
		return domain.ErrLoraVerification
	}
	for index, job := range params.Jobs {
		member := verification.Members[index]
		if job.ParentJobID != nil || job.Prepared.Seed != member.Seed || job.Position != member.Position || job.ExperimentValue != member.Weight ||
			job.Prepared.TemplateID != params.TemplateID || job.Prepared.WorkflowID != params.WorkflowID || job.Prepared.ModelName != params.ModelName {
			return domain.ErrLoraVerification
		}
	}
	return nil
}

func verifyExistingLoraBatch(ctx context.Context, tx *sql.Tx, mode domain.GenerationBatchMode, id int64, params domain.CreateGenerationBatchParams) error {
	if mode != domain.GenerationBatchLoraVerification && params.LoraVerification == nil {
		return nil
	}
	if mode != domain.GenerationBatchLoraVerification || params.LoraVerification == nil {
		return ErrGenerationBatchConflict
	}
	verification := params.LoraVerification
	var matches bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lora_verifications WHERE batch_id=$1 AND user_id=$2 AND
  training_public_id=$3 AND request_sha256=$4 AND model_id=$5 AND profile_sha256=$6 AND artifact_name=$7 AND artifact_bytes=$8 AND artifact_sha256=$9)`,
		id, params.UserID, verification.TrainingPublicID, verification.RequestSHA256, verification.ModelID, verification.ProfileSHA256, verification.Artifact.Name, verification.Artifact.Bytes, verification.Artifact.SHA256).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return ErrGenerationBatchConflict
	}
	return nil
}

func reserveLoraVerificationSource(ctx context.Context, tx *sql.Tx, params domain.CreateGenerationBatchParams) (domain.LoraTrainingJob, error) {
	verification := params.LoraVerification
	job, err := scanLoraTrainingJob(tx.QueryRowContext(ctx, `SELECT `+loraTrainingColumns+` FROM lora_training_jobs WHERE id=$1 AND user_id=$2 FOR SHARE`, verification.TrainingJobID, params.UserID))
	if errors.Is(err, sql.ErrNoRows) {
		return job, ErrLoraVerificationSource
	}
	if err != nil {
		return job, err
	}
	if job.State != domain.LoraTrainingCompleted || job.PublicID != verification.TrainingPublicID || job.AgentJobID == "" || job.Family != verification.Family || job.Provenance == nil || job.Provenance.Validate() != nil || job.Provenance.Artifact == nil ||
		job.Provenance.ProfileSHA256 != verification.ProfileSHA256 || *job.Provenance.Artifact != verification.Artifact || job.ArtifactName != verification.Artifact.Name || job.ArtifactBytes != verification.Artifact.Bytes {
		return job, ErrLoraVerificationSource
	}
	return job, nil
}

func insertLoraVerification(ctx context.Context, tx *sql.Tx, batchID int64, params domain.CreateGenerationBatchParams, job domain.LoraTrainingJob) error {
	v := params.LoraVerification
	provenance, err := json.Marshal(job.Provenance)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO lora_verifications
  (batch_id,user_id,training_job_id,training_public_id,training_name,family,profile_sha256,artifact_name,artifact_bytes,artifact_sha256,
   training_provenance,dataset_snapshot_id,dataset_snapshot_hash,model_id,request_sha256,plan_cipher)
  VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		batchID, params.UserID, job.ID, job.PublicID, job.Name, job.Family, v.ProfileSHA256, v.Artifact.Name, v.Artifact.Bytes, v.Artifact.SHA256,
		provenance, job.DatasetSnapshotID, job.DatasetSnapshotHash, v.ModelID, v.RequestSHA256, v.PlanCipher)
	return err
}

func insertLoraVerificationMember(ctx context.Context, tx *sql.Tx, batchID, jobID int64, publicID string, member domain.LoraVerificationMemberSpec) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO lora_verification_members(batch_id,position,job_id,job_public_id,case_index,variant_index,seed,weight)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, batchID, member.Position, jobID, publicID, member.CaseIndex, member.VariantIndex, member.Seed, member.Weight)
	return err
}

// Membership survives source/child deletion without inventing replacement jobs.
func (s *Store) LoraVerificationByBatch(ctx context.Context, userID int64, batchPublicID string) (domain.LoraVerification, error) {
	var v domain.LoraVerification
	var trainingID sql.NullInt64
	var totalCount int
	var provenance []byte
	err := s.db.QueryRowContext(ctx, `SELECT v.batch_id,b.public_id,v.training_job_id,v.training_public_id,v.training_name,v.family,
  v.profile_sha256,v.artifact_name,v.artifact_bytes,v.artifact_sha256,v.training_provenance,v.dataset_snapshot_id,v.dataset_snapshot_hash,
		v.model_id,v.request_sha256,v.plan_cipher,v.created_at,b.total_count FROM lora_verifications v JOIN generation_batches b ON b.id=v.batch_id
		WHERE v.user_id=$1 AND b.user_id=$1 AND b.public_id=$2 AND b.experiment_mode='lora_verification'`, userID, batchPublicID).Scan(
		&v.BatchID, &v.BatchPublicID, &trainingID, &v.TrainingPublicID, &v.TrainingName, &v.Family, &v.ProfileSHA256, &v.Artifact.Name, &v.Artifact.Bytes, &v.Artifact.SHA256,
		&provenance, &v.DatasetSnapshotID, &v.DatasetSnapshotHash, &v.ModelID, &v.RequestSHA256, &v.PlanCipher, &v.CreatedAt, &totalCount)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(provenance, &v.TrainingProvenance); err != nil || v.TrainingProvenance == nil || v.TrainingProvenance.Validate() != nil {
		return domain.LoraVerification{}, domain.ErrLoraProvenance
	}
	if trainingID.Valid {
		id := trainingID.Int64
		v.TrainingJobID = &id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.position,m.case_index,m.variant_index,m.seed,m.weight,m.job_id,m.job_public_id,COALESCE(j.state,'missing')
 FROM lora_verification_members m JOIN lora_verifications v ON v.batch_id=m.batch_id
 LEFT JOIN generation_jobs j ON j.id=m.job_id AND j.user_id=v.user_id AND j.batch_id=m.batch_id AND j.public_id=m.job_public_id
 WHERE m.batch_id=$1 AND v.user_id=$2 ORDER BY m.position`, v.BatchID, userID)
	if err != nil {
		return domain.LoraVerification{}, err
	}
	defer rows.Close()
	v.Members = []domain.LoraVerificationMember{}
	for rows.Next() {
		var member domain.LoraVerificationMember
		var jobID sql.NullInt64
		if err := rows.Scan(&member.Position, &member.CaseIndex, &member.VariantIndex, &member.Seed, &member.Weight, &jobID, &member.JobPublicID, &member.State); err != nil {
			return domain.LoraVerification{}, err
		}
		if jobID.Valid && member.State != "missing" {
			id := jobID.Int64
			member.JobID = &id
		}
		v.Members = append(v.Members, member)
	}
	if err := rows.Err(); err != nil {
		return domain.LoraVerification{}, err
	}
	if len(v.Members) != totalCount || totalCount < 2 || totalCount > 20 {
		return domain.LoraVerification{}, domain.ErrLoraVerification
	}
	return v, nil
}

func (s *Store) ListLoraVerificationBatches(ctx context.Context, userID int64, trainingPublicID string, before int64, limit int) ([]string, error) {
	if limit < 1 || limit > 30 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `SELECT b.public_id FROM lora_verifications v JOIN generation_batches b ON b.id=v.batch_id
 WHERE v.user_id=$1 AND b.user_id=$1 AND v.training_public_id=$2 AND ($3::bigint=0 OR v.batch_id<$3)
 ORDER BY v.batch_id DESC LIMIT $4`, userID, trainingPublicID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
