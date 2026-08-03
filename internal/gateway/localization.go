package gateway

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

func roleLabel(role string) string {
	switch role {
	case "admin":
		return "администратор"
	case "user":
		return "пользователь"
	default:
		return role
	}
}

func inviteStatusLabel(status string) string {
	switch status {
	case "active":
		return "активно"
	case "expired":
		return "истекло"
	case "used":
		return "использовано"
	case "revoked":
		return "отозвано"
	default:
		return status
	}
}

func auditActionLabel(action string) string {
	labels := map[string]string{
		"admin_login":                 "вход администратора",
		"admin_user_password_changed": "администратор сменил пароль пользователя",
		"invite_created":              "создано приглашение",
		"invite_deleted":              "приглашение удалено",
		"invite_revoked":              "приглашение отозвано",
		"invite_unrevoked":            "приглашение возвращено",
		"invite_used":                 "приглашение использовано",
		"mining_started":              "майнинг запущен",
		"mining_stopped":              "майнинг остановлен",
		"miner_profile_created":       "добавлен профиль майнинга",
		"miner_profile_default":       "выбран основной профиль майнинга",
		"miner_profile_enable":        "профиль майнинга включён",
		"miner_profile_disable":       "профиль майнинга отключён",
		"miner_profile_delete":        "профиль майнинга удалён",
		"session_revoked":             "сессия завершена администратором",
		"sessions_revoked":            "сессии пользователя завершены",
		"user_disabled":               "пользователь отключён",
		"user_enabled":                "пользователь включён",
		"user_login_failed":           "неудачная попытка входа",
		"user_login_success":          "успешный вход",
		"user_logout":                 "выход из системы",
		"user_password_changed":       "пользователь сменил пароль",
		"user_service_access_updated": "изменён доступ к сервисам",
		"user_session_revoked":        "пользователь завершил сессию",
		"user_sessions_revoked":       "пользователь завершил остальные сессии",
		"user_unlocked":               "снята блокировка входа",
	}
	if label, ok := labels[action]; ok {
		return label
	}
	return action
}

func auditTargetLabel(target string) string {
	switch target {
	case "user":
		return "пользователь"
	case "session":
		return "сессия"
	case "invite":
		return "приглашение"
	case "miner":
		return "профиль майнинга"
	default:
		return target
	}
}

func auditMetadataLabel(raw string) string {
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || len(metadata) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", auditMetadataKeyLabel(key), auditMetadataValueLabel(key, metadata[key])))
	}
	return strings.Join(parts, "; ")
}

func auditMetadataKeyLabel(key string) string {
	labels := map[string]string{
		"comfyui":          "ComfyUI",
		"count":            "количество",
		"expires_at":       "срок действия",
		"grant_comfyui":    "доступ к ComfyUI",
		"grant_openwebui":  "доступ к OpenWebUI",
		"max_uses":         "максимум активаций",
		"name":             "название",
		"openwebui":        "OpenWebUI",
		"process_name":     "процесс",
		"running":          "состояние",
		"script_path":      "скрипт",
		"reason":           "причина",
		"sessions_revoked": "завершено сессий",
		"username":         "логин",
	}
	if label, ok := labels[key]; ok {
		return label
	}
	return key
}

func auditMetadataValueLabel(key string, value any) string {
	if key == "reason" {
		if reason, ok := value.(string); ok {
			labels := map[string]string{
				"account_locked":      "аккаунт заблокирован",
				"invalid_credentials": "неверные данные",
				"rate_limited":        "слишком много попыток",
			}
			if label, exists := labels[reason]; exists {
				return label
			}
		}
	}
	switch typed := value.(type) {
	case bool:
		if typed {
			return "да"
		}
		return "нет"
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, typed); err == nil {
			return parsed.Local().Format("02.01.2006 15:04")
		}
		return typed
	default:
		return fmt.Sprint(value)
	}
}
