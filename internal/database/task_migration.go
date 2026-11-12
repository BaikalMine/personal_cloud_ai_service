package database

var taskReadModelMigration = migration{
	version: 71, name: "owner_task_read_model", statements: []string{
		`CREATE TABLE user_task_revisions (
				user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
				revision BIGINT NOT NULL DEFAULT 1 CHECK(revision>0)
			)`,
		`INSERT INTO user_task_revisions(user_id) SELECT id FROM users`,
		`CREATE FUNCTION advance_owner_task_revision() RETURNS trigger LANGUAGE plpgsql AS $$
			DECLARE old_owner BIGINT; new_owner BIGINT; owner BIGINT;
			BEGIN
				IF TG_OP <> 'INSERT' THEN old_owner := OLD.user_id; END IF;
				IF TG_OP <> 'DELETE' THEN new_owner := NEW.user_id; END IF;
				IF TG_OP = 'UPDATE' AND OLD IS NOT DISTINCT FROM NEW THEN RETURN NULL; END IF;
				FOR owner IN SELECT DISTINCT id FROM unnest(ARRAY[old_owner,new_owner]) id
					WHERE id IS NOT NULL ORDER BY id LOOP
					INSERT INTO user_task_revisions(user_id,revision)
						SELECT id,1 FROM users WHERE id=owner
					ON CONFLICT(user_id) DO UPDATE SET revision=user_task_revisions.revision+1;
				END LOOP;
				RETURN NULL;
			END $$`,
		`CREATE TRIGGER generation_task_revision AFTER INSERT OR UPDATE OR DELETE ON generation_jobs
				FOR EACH ROW EXECUTE FUNCTION advance_owner_task_revision()`,
		`CREATE TRIGGER training_task_revision AFTER INSERT OR UPDATE OR DELETE ON lora_training_jobs
				FOR EACH ROW EXECUTE FUNCTION advance_owner_task_revision()`,
		`CREATE TRIGGER caption_task_revision AFTER INSERT OR UPDATE OR DELETE ON lora_caption_jobs
				FOR EACH ROW EXECUTE FUNCTION advance_owner_task_revision()`,
		`CREATE TRIGGER dataset_task_revision AFTER INSERT OR UPDATE OR DELETE ON lora_datasets
				FOR EACH ROW EXECUTE FUNCTION advance_owner_task_revision()`,
		`CREATE TRIGGER batch_task_revision AFTER INSERT OR UPDATE OR DELETE ON generation_batches
			FOR EACH ROW EXECUTE FUNCTION advance_owner_task_revision()`,
		`CREATE FUNCTION advance_task_access_revision() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			INSERT INTO user_task_revisions(user_id,revision) VALUES(NEW.id,1)
			ON CONFLICT(user_id) DO UPDATE SET revision=user_task_revisions.revision+1;
			RETURN NULL;
		END $$`,
		`CREATE TRIGGER task_access_revision AFTER UPDATE OF role,can_use_quick_generation,can_train_image_lora,disabled,account_expires_at ON users
			FOR EACH ROW WHEN ((OLD.role,OLD.can_use_quick_generation,OLD.can_train_image_lora,OLD.disabled,OLD.account_expires_at)
			IS DISTINCT FROM (NEW.role,NEW.can_use_quick_generation,NEW.can_train_image_lora,NEW.disabled,NEW.account_expires_at))
			EXECUTE FUNCTION advance_task_access_revision()`,
		`CREATE INDEX generation_tasks_owner_idx ON generation_jobs(user_id,created_at DESC,public_id COLLATE "C" DESC)`,
		`CREATE INDEX training_tasks_owner_idx ON lora_training_jobs(user_id,created_at DESC,public_id COLLATE "C" DESC)`,
		`CREATE INDEX caption_tasks_owner_idx ON lora_caption_jobs(user_id,created_at DESC,id COLLATE "C" DESC)`,
	},
}
