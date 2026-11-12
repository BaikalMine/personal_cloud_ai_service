package database

var generationCollectionContextMigration = migration{
	version: 94, name: "generation_working_collections", statements: []string{
		`CREATE SEQUENCE generation_collection_revision_seq`,
		`ALTER TABLE generation_media_collections
		 ADD COLUMN revision BIGINT NOT NULL DEFAULT nextval('generation_collection_revision_seq'),
		 ADD COLUMN context_cipher BYTEA,
		 ADD COLUMN context_saved_at TIMESTAMPTZ,
		 ADD CONSTRAINT generation_collection_context_consistent CHECK (
		   (context_cipher IS NULL AND context_saved_at IS NULL) OR
		   (context_cipher IS NOT NULL AND context_saved_at IS NOT NULL AND octet_length(context_cipher) BETWEEN 1 AND 196608))`,
		`CREATE TABLE generation_collection_jobs (
		 job_id BIGINT PRIMARY KEY REFERENCES generation_jobs(id) ON DELETE CASCADE,
		 collection_id BIGINT NOT NULL REFERENCES generation_media_collections(id) ON DELETE CASCADE,
		 created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE INDEX generation_collection_jobs_collection_idx ON generation_collection_jobs(collection_id,job_id)`,
		`CREATE FUNCTION attach_generation_collection_output() RETURNS trigger AS $$
		BEGIN
		 INSERT INTO generation_media_collection_items(collection_id,media_id)
		 SELECT collection.id,NEW.id FROM content_events event
		 JOIN generation_jobs job ON job.id=event.generation_job_id AND job.user_id=event.user_id
		 JOIN generation_collection_jobs binding ON binding.job_id=job.id
		 JOIN generation_media_collections collection ON collection.id=binding.collection_id AND collection.user_id=job.user_id
		 WHERE event.id=NEW.event_id AND event.service='comfyui' AND event.kind='comfyui_prompt'
		   AND event.expires_at>now() AND NEW.expires_at>now() AND NEW.profile_hidden_at IS NULL
		 ON CONFLICT DO NOTHING;
		 RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`,
		`CREATE TRIGGER generation_collection_output AFTER INSERT ON content_media
		 FOR EACH ROW EXECUTE FUNCTION attach_generation_collection_output()`,
	},
}
