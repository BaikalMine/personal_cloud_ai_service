package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"ai-access-gateway/internal/domain"
)

const userNotificationColumns = `
	n.id,n.user_id,n.generation_job_id,j.public_id,n.kind,n.title,n.message,n.href,n.read_at,n.created_at`

type notificationScanner interface {
	Scan(dest ...any) error
}

func scanUserNotification(scanner notificationScanner) (domain.UserNotification, error) {
	var notification domain.UserNotification
	var kind string
	var readAt sql.NullTime
	if err := scanner.Scan(
		&notification.ID, &notification.UserID, &notification.GenerationJobID, &notification.GenerationJobPublicID,
		&kind, &notification.Title, &notification.Message, &notification.Href, &readAt, &notification.CreatedAt,
	); err != nil {
		return domain.UserNotification{}, err
	}
	notification.Kind = domain.NotificationKind(kind)
	if !notification.Kind.Valid() {
		return domain.UserNotification{}, fmt.Errorf("unknown notification kind %q", kind)
	}
	if readAt.Valid {
		value := readAt.Time
		notification.ReadAt = &value
	}
	return notification, nil
}

func (s *Store) UserNotificationSummary(ctx context.Context, userID int64) (domain.UserNotificationSummary, error) {
	var summary domain.UserNotificationSummary
	err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(r.revision,0),
		(SELECT count(*) FROM user_notifications n WHERE n.user_id=u.id AND n.read_at IS NULL),
		(SELECT count(*) FROM generation_jobs j WHERE j.user_id=u.id AND j.state NOT IN ('completed','failed','cancelled','expired')),
		COALESCE(p.in_app_enabled,true),COALESCE(p.success_enabled,true),COALESCE(p.browser_enabled,false),
		COALESCE(p.updated_at,u.created_at)
		FROM users u
		LEFT JOIN user_notification_preferences p ON p.user_id=u.id
		LEFT JOIN user_notification_revision r ON r.user_id=u.id
		WHERE u.id=$1`, userID).Scan(
		&summary.Revision, &summary.UnreadCount, &summary.ActiveCount,
		&summary.Preferences.InAppEnabled, &summary.Preferences.SuccessEnabled,
		&summary.Preferences.BrowserEnabled, &summary.Preferences.UpdatedAt,
	)
	return summary, err
}

func (s *Store) UserNotificationRevision(ctx context.Context, userID int64) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM user_notification_revision WHERE user_id=$1),0)`, userID).Scan(&revision)
	return revision, err
}

func (s *Store) ListUserNotifications(ctx context.Context, userID int64, limit int) ([]domain.UserNotification, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+userNotificationColumns+`
		FROM user_notifications n
		JOIN generation_jobs j ON j.id=n.generation_job_id
		WHERE n.user_id=$1
		ORDER BY n.created_at DESC,n.id DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notifications := make([]domain.UserNotification, 0, limit)
	for rows.Next() {
		notification, err := scanUserNotification(rows)
		if err != nil {
			return nil, err
		}
		notifications = append(notifications, notification)
	}
	return notifications, rows.Err()
}

func (s *Store) SetUserNotificationPreferences(ctx context.Context, userID int64, preferences domain.UserNotificationPreferences) (domain.UserNotificationPreferences, bool, error) {
	if !preferences.InAppEnabled {
		preferences.BrowserEnabled = false
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.UserNotificationPreferences{}, false, err
	}
	defer tx.Rollback()
	var stored domain.UserNotificationPreferences
	err = tx.QueryRowContext(ctx, `INSERT INTO user_notification_preferences(user_id,in_app_enabled,success_enabled,browser_enabled)
		VALUES($1,$2,$3,$4)
		ON CONFLICT(user_id) DO UPDATE SET
			in_app_enabled=EXCLUDED.in_app_enabled,
			success_enabled=EXCLUDED.success_enabled,
			browser_enabled=EXCLUDED.browser_enabled,
			updated_at=now()
		WHERE (user_notification_preferences.in_app_enabled,user_notification_preferences.success_enabled,user_notification_preferences.browser_enabled)
			IS DISTINCT FROM (EXCLUDED.in_app_enabled,EXCLUDED.success_enabled,EXCLUDED.browser_enabled)
		RETURNING in_app_enabled,success_enabled,browser_enabled,updated_at`, userID,
		preferences.InAppEnabled, preferences.SuccessEnabled, preferences.BrowserEnabled,
	).Scan(&stored.InAppEnabled, &stored.SuccessEnabled, &stored.BrowserEnabled, &stored.UpdatedAt)
	changed := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT in_app_enabled,success_enabled,browser_enabled,updated_at
			FROM user_notification_preferences WHERE user_id=$1`, userID).Scan(
			&stored.InAppEnabled, &stored.SuccessEnabled, &stored.BrowserEnabled, &stored.UpdatedAt,
		)
	}
	if err != nil {
		return domain.UserNotificationPreferences{}, false, err
	}
	var notificationsMarked int64
	if !stored.InAppEnabled {
		result, err := tx.ExecContext(ctx, `UPDATE user_notifications SET read_at=now()
			WHERE user_id=$1 AND read_at IS NULL`, userID)
		if err != nil {
			return domain.UserNotificationPreferences{}, false, err
		}
		notificationsMarked, err = result.RowsAffected()
		if err != nil {
			return domain.UserNotificationPreferences{}, false, err
		}
	}
	if changed || notificationsMarked > 0 {
		if err := incrementUserNotificationRevision(ctx, tx, userID); err != nil {
			return domain.UserNotificationPreferences{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.UserNotificationPreferences{}, false, err
	}
	return stored, changed || notificationsMarked > 0, nil
}

func (s *Store) MarkUserNotificationRead(ctx context.Context, userID, notificationID int64) (bool, error) {
	changed, err := s.markUserNotificationsRead(ctx, userID, notificationID)
	return changed > 0, err
}

func (s *Store) MarkAllUserNotificationsRead(ctx context.Context, userID int64) (int64, error) {
	return s.markUserNotificationsRead(ctx, userID, 0)
}

func (s *Store) markUserNotificationsRead(ctx context.Context, userID, notificationID int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	query := `UPDATE user_notifications SET read_at=now() WHERE user_id=$1 AND read_at IS NULL`
	args := []any{userID}
	if notificationID > 0 {
		query += ` AND id=$2`
		args = append(args, notificationID)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if changed > 0 {
		if err := incrementUserNotificationRevision(ctx, tx, userID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return changed, nil
}

func createGenerationJobNotification(ctx context.Context, tx *sql.Tx, job domain.GenerationJob) error {
	if job.UserID == nil || (job.State != domain.GenerationJobCompleted && job.State != domain.GenerationJobFailed) {
		return nil
	}
	var inAppEnabled, successEnabled bool
	if err := tx.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT in_app_enabled FROM user_notification_preferences WHERE user_id=$1),true),
		COALESCE((SELECT success_enabled FROM user_notification_preferences WHERE user_id=$1),true)`, *job.UserID).Scan(&inAppEnabled, &successEnabled); err != nil {
		return err
	}
	if !inAppEnabled || (job.State == domain.GenerationJobCompleted && !successEnabled) {
		return nil
	}
	kind := domain.NotificationGenerationCompleted
	title := "Генерация готова"
	message := "Результат сохранён и готов к просмотру."
	if model := strings.TrimSpace(job.ModelName); model != "" {
		message = "Результат " + model + " сохранён и готов к просмотру."
	}
	if job.State == domain.GenerationJobFailed {
		kind = domain.NotificationGenerationFailed
		title = "Генерация завершилась с ошибкой"
		message = strings.TrimSpace(job.StatusMessage)
		if message == "" {
			message = "Откройте задачу, чтобы посмотреть причину и повторить запуск."
		}
	}
	message = truncateNotificationText(message, 500)
	href := "/generate?job=" + url.QueryEscape(job.PublicID)
	var notificationID int64
	err := tx.QueryRowContext(ctx, `INSERT INTO user_notifications(user_id,generation_job_id,kind,title,message,href)
		VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(generation_job_id) DO NOTHING
		RETURNING id`, *job.UserID, job.ID, kind, title, message, href).Scan(&notificationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return nil
}

func truncateNotificationText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

func incrementUserNotificationRevision(ctx context.Context, tx *sql.Tx, userID int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO user_notification_revision(user_id,revision,changed_at)
		VALUES($1,1,now())
		ON CONFLICT(user_id) DO UPDATE SET revision=user_notification_revision.revision+1,changed_at=now()`, userID)
	return err
}
