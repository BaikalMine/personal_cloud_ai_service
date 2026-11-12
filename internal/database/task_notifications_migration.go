package database

var taskNotificationsMigration = migration{
	version: 72, name: "task_notifications_and_caption_series", statements: []string{
		`ALTER TABLE lora_caption_jobs ADD COLUMN run_version BIGINT NOT NULL DEFAULT 1 CHECK(run_version>0)`,
		`CREATE TABLE lora_caption_series (
			id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 16 AND 96),
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			dataset_id TEXT REFERENCES lora_datasets(id) ON DELETE SET NULL,
			name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
			total_count INTEGER NOT NULL CHECK(total_count BETWEEN 1 AND 100),
			completed_count INTEGER NOT NULL DEFAULT 0, failed_count INTEGER NOT NULL DEFAULT 0,
			cancelled_count INTEGER NOT NULL DEFAULT 0, unavailable_count INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL DEFAULT 'running' CHECK(state IN ('running','completed','failed','cancelled','partial')),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			finished_at TIMESTAMPTZ, expires_at TIMESTAMPTZ NOT NULL DEFAULT now()+interval '30 days',
			CHECK(completed_count>=0 AND failed_count>=0 AND cancelled_count>=0 AND unavailable_count>=0
			AND completed_count+failed_count+cancelled_count+unavailable_count<=total_count)
		)`,
		`CREATE INDEX caption_series_owner_idx ON lora_caption_series(user_id,created_at DESC,id)`,
		`CREATE TABLE lora_caption_series_members (
			series_id TEXT NOT NULL REFERENCES lora_caption_series(id) ON DELETE CASCADE,
			job_id TEXT REFERENCES lora_caption_jobs(id) ON DELETE SET NULL,
			source_job_id TEXT NOT NULL, run_version BIGINT NOT NULL CHECK(run_version>0),
			image_id TEXT NOT NULL, position INTEGER NOT NULL CHECK(position BETWEEN 1 AND 100),
			state TEXT NOT NULL CHECK(state IN ('queued','running','completed','failed','cancelled','unavailable')),
			PRIMARY KEY(series_id,source_job_id), UNIQUE(series_id,position)
		)`,
		`CREATE INDEX caption_series_member_job_idx ON lora_caption_series_members(job_id,run_version)`,
		`ALTER TABLE user_notifications ALTER COLUMN generation_job_id DROP NOT NULL,
			ADD COLUMN training_job_id BIGINT REFERENCES lora_training_jobs(id) ON DELETE CASCADE,
			ADD COLUMN caption_series_id TEXT REFERENCES lora_caption_series(id) ON DELETE CASCADE,
			ADD CONSTRAINT notification_one_source CHECK(num_nonnulls(generation_job_id,training_job_id,caption_series_id)=1),
			ADD CONSTRAINT notification_training_unique UNIQUE(training_job_id),
			ADD CONSTRAINT notification_caption_series_unique UNIQUE(caption_series_id),
			DROP CONSTRAINT user_notifications_kind_check`,
		`ALTER TABLE user_notifications ADD CONSTRAINT user_notifications_kind_check CHECK(
			(generation_job_id IS NOT NULL AND kind IN ('generation_completed','generation_failed')) OR
			(training_job_id IS NOT NULL AND kind IN ('lora_training_completed','lora_training_failed')) OR
			(caption_series_id IS NOT NULL AND kind IN ('lora_caption_series_completed','lora_caption_series_failed','lora_caption_series_cancelled'))
		)`,
		`CREATE FUNCTION advance_task_notification_revision() RETURNS trigger LANGUAGE plpgsql AS $$
		DECLARE owner BIGINT;
		BEGIN
			IF TG_OP='UPDATE' AND (OLD.state,OLD.user_id) IS NOT DISTINCT FROM (NEW.state,NEW.user_id) THEN RETURN NULL; END IF;
			IF TG_OP='DELETE' THEN owner := OLD.user_id; ELSE owner := NEW.user_id; END IF;
			INSERT INTO user_notification_revision(user_id,revision,changed_at)
			SELECT id,1,now() FROM users WHERE id=owner
			ON CONFLICT(user_id) DO UPDATE SET revision=user_notification_revision.revision+1,changed_at=now();
			RETURN NULL;
		END $$`,
		`CREATE TRIGGER training_notification_revision AFTER INSERT OR DELETE OR UPDATE OF state,user_id ON lora_training_jobs
			FOR EACH ROW EXECUTE FUNCTION advance_task_notification_revision()`,
		`CREATE TRIGGER caption_notification_revision AFTER INSERT OR DELETE OR UPDATE OF state,user_id ON lora_caption_jobs
			FOR EACH ROW EXECUTE FUNCTION advance_task_notification_revision()`,
		`CREATE TRIGGER caption_series_notification_revision AFTER INSERT OR DELETE OR UPDATE OF state,user_id ON lora_caption_series
			FOR EACH ROW EXECUTE FUNCTION advance_task_notification_revision()`,
		`CREATE TRIGGER caption_series_task_revision AFTER INSERT OR UPDATE OR DELETE ON lora_caption_series
			FOR EACH ROW EXECUTE FUNCTION advance_owner_task_revision()`,
		`CREATE FUNCTION notify_training_result() RETURNS trigger LANGUAGE plpgsql AS $$
		DECLARE enabled BOOLEAN; successes BOOLEAN; kind TEXT; title TEXT; message TEXT;
		BEGIN
			IF NEW.user_id IS NULL OR NEW.state NOT IN ('completed','failed') OR OLD.state=NEW.state THEN RETURN NULL; END IF;
			SELECT COALESCE(p.in_app_enabled,true),COALESCE(p.success_enabled,true) INTO enabled,successes
			FROM users u LEFT JOIN user_notification_preferences p ON p.user_id=u.id WHERE u.id=NEW.user_id;
			IF NOT FOUND OR NOT enabled OR (NEW.state='completed' AND NOT successes) THEN RETURN NULL; END IF;
			IF NEW.state='completed' THEN
				kind := 'lora_training_completed'; title := 'Обучение LoRA завершено'; message := 'Откройте задачу, чтобы скачать LoRA и проверить результат.';
			ELSE
				kind := 'lora_training_failed'; title := 'Не удалось обучить LoRA'; message := 'Откройте задачу, чтобы посмотреть причину и сохранённые параметры.';
			END IF;
			INSERT INTO user_notifications(user_id,training_job_id,kind,title,message,href)
			VALUES(NEW.user_id,NEW.id,kind,title,left(NEW.name||'. '||message,500),'/tasks?task=lora_training:'||NEW.public_id)
			ON CONFLICT(training_job_id) DO NOTHING;
			RETURN NULL;
		END $$`,
		`CREATE TRIGGER training_result_notification AFTER UPDATE OF state ON lora_training_jobs
			FOR EACH ROW EXECUTE FUNCTION notify_training_result()`,
		`CREATE FUNCTION refresh_caption_series(series_key TEXT) RETURNS void LANGUAGE plpgsql AS $$
		DECLARE series lora_caption_series%ROWTYPE; members INTEGER; done INTEGER; failed INTEGER; cancelled INTEGER; unavailable INTEGER;
			next_state TEXT; kind TEXT; title TEXT; enabled BOOLEAN; successes BOOLEAN;
		BEGIN
			SELECT * INTO series FROM lora_caption_series WHERE id=series_key FOR UPDATE;
			IF NOT FOUND OR series.state<>'running' THEN RETURN; END IF;
			SELECT count(*),count(*) FILTER(WHERE state='completed'),count(*) FILTER(WHERE state='failed'),
				count(*) FILTER(WHERE state='cancelled'),count(*) FILTER(WHERE state='unavailable')
			INTO members,done,failed,cancelled,unavailable FROM lora_caption_series_members WHERE series_id=series_key;
			IF members<>series.total_count THEN RAISE EXCEPTION 'incomplete caption series membership'; END IF;
			next_state := 'running';
			IF done+failed+cancelled+unavailable=members THEN
				next_state := CASE WHEN done=members THEN 'completed' WHEN cancelled=members THEN 'cancelled' WHEN failed+unavailable=members THEN 'failed' ELSE 'partial' END;
			END IF;
			UPDATE lora_caption_series SET completed_count=done,failed_count=failed,cancelled_count=cancelled,unavailable_count=unavailable,
				state=next_state,updated_at=now(),finished_at=CASE WHEN next_state<>'running' THEN now() END,
				expires_at=CASE WHEN next_state<>'running' THEN now()+interval '30 days' ELSE expires_at END
			WHERE id=series_key;
			IF next_state='running' THEN RETURN; END IF;
			SELECT COALESCE(p.in_app_enabled,true),COALESCE(p.success_enabled,true) INTO enabled,successes
			FROM users u LEFT JOIN user_notification_preferences p ON p.user_id=u.id WHERE u.id=series.user_id;
			IF NOT FOUND OR NOT enabled OR (next_state='completed' AND NOT successes) THEN RETURN; END IF;
			IF failed+unavailable>0 THEN kind := 'lora_caption_series_failed'; title := 'Не все описания готовы';
			ELSIF cancelled>0 THEN kind := 'lora_caption_series_cancelled'; title := 'Серия описаний требует проверки';
			ELSE kind := 'lora_caption_series_completed'; title := 'Описания кадров готовы'; END IF;
			INSERT INTO user_notifications(user_id,caption_series_id,kind,title,message,href)
			VALUES(series.user_id,series.id,kind,title,
				format('Готово: %s из %s. Ошибки: %s. Отменено: %s. Недоступно: %s.',done,members,failed,cancelled,unavailable),
				'/tasks?task=lora_caption_series:'||series.id) ON CONFLICT(caption_series_id) DO NOTHING;
		END $$`,
		`CREATE FUNCTION advance_caption_run_version() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.state IN ('completed','failed','cancelled') AND NEW.state IN ('queued','running') THEN NEW.run_version := OLD.run_version+1;
			ELSE NEW.run_version := OLD.run_version; END IF;
			RETURN NEW;
		END $$`,
		`CREATE TRIGGER caption_run_version BEFORE UPDATE ON lora_caption_jobs FOR EACH ROW EXECUTE FUNCTION advance_caption_run_version()`,
		`CREATE FUNCTION update_caption_series_members() RETURNS trigger LANGUAGE plpgsql AS $$
		DECLARE series_key TEXT;
		BEGIN
			IF TG_OP='UPDATE' AND OLD.state=NEW.state AND OLD.run_version=NEW.run_version THEN RETURN NULL; END IF;
			FOR series_key IN SELECT DISTINCT m.series_id FROM lora_caption_series_members m
				JOIN lora_caption_series s ON s.id=m.series_id
				WHERE m.source_job_id=OLD.id AND m.run_version=OLD.run_version AND s.state='running'
				AND m.state IN ('queued','running') ORDER BY m.series_id LOOP
				UPDATE lora_caption_series_members SET state=CASE WHEN TG_OP='DELETE' THEN 'unavailable' ELSE NEW.state END
				WHERE series_id=series_key AND source_job_id=OLD.id AND run_version=OLD.run_version;
				PERFORM refresh_caption_series(series_key);
			END LOOP;
			RETURN NULL;
		END $$`,
		`CREATE TRIGGER caption_series_members_update AFTER UPDATE OR DELETE ON lora_caption_jobs
			FOR EACH ROW EXECUTE FUNCTION update_caption_series_members()`,
	},
}
