package gateway

import (
	"path"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

const activityMergeWindow = 2 * time.Minute

// prepareUserActivities turns high-volume proxy telemetry into a short list of user actions.
// The original records remain available to admin analytics and the content/audit sections.
func prepareUserActivities(records []domain.Activity, limit int) []domain.Activity {
	if limit < 1 {
		return nil
	}
	activities := make([]domain.Activity, 0, limit)
	for _, record := range records {
		if isActivityNoise(record) {
			continue
		}
		record.ServiceLabel, record.Summary = describeActivity(record)
		record.Count = 1
		if len(activities) > 0 && canMergeActivities(activities[len(activities)-1], record) {
			merged := &activities[len(activities)-1]
			merged.Count++
			merged.Bytes += record.Bytes
			merged.Duration = ((merged.Duration * int64(merged.Count-1)) + record.Duration) / int64(merged.Count)
			if record.Status >= 400 && merged.Status < 400 {
				merged.Status = record.Status
			}
			continue
		}
		activities = append(activities, record)
		if len(activities) == limit {
			break
		}
	}
	return activities
}

func canMergeActivities(previous, current domain.Activity) bool {
	return previous.Service == current.Service &&
		previous.Summary == current.Summary &&
		previous.Status/100 == current.Status/100 &&
		previous.CreatedAt.Sub(current.CreatedAt) <= activityMergeWindow
}

func isActivityNoise(activity domain.Activity) bool {
	if activity.WebSocket {
		return true
	}
	requestPath := activityPath(activity.Path)
	lowerPath := strings.ToLower(requestPath)
	if requestPath == "" || requestPath == "/" {
		return true
	}
	if isStaticActivityPath(lowerPath) {
		return true
	}
	if activity.Method == "GET" && isBackgroundActivityPath(lowerPath) {
		return true
	}
	return false
}

func describeActivity(activity domain.Activity) (string, string) {
	requestPath := strings.ToLower(activityPath(activity.Path))
	switch activity.Service {
	case "comfyui":
		switch {
		case strings.HasSuffix(requestPath, "/prompt") && activity.Method == "POST":
			return "ComfyUI", "Запуск генерации"
		case strings.Contains(requestPath, "/interrupt") && activity.Method == "POST":
			return "ComfyUI", "Остановка генерации"
		case strings.Contains(requestPath, "/history"):
			return "ComfyUI", "Просмотр истории генераций"
		case strings.Contains(requestPath, "/view"):
			return "ComfyUI", "Просмотр результата"
		case strings.Contains(requestPath, "/userdata"):
			return "ComfyUI", "Работа с файлами рабочего пространства"
		default:
			return "ComfyUI", "Работа в рабочей области"
		}
	case "openwebui":
		switch {
		case strings.Contains(requestPath, "/chat/completions") || strings.HasSuffix(requestPath, "/api/chat"):
			return "OpenWebUI", "Сообщение нейросети"
		case strings.Contains(requestPath, "/chats"):
			return "OpenWebUI", "Работа с чатами"
		case strings.Contains(requestPath, "/files") || strings.Contains(requestPath, "/documents"):
			return "OpenWebUI", "Работа с файлами"
		default:
			return "OpenWebUI", "Работа в рабочей области"
		}
	default:
		return activity.Service, "Работа с сервисом"
	}
}

func activityPath(raw string) string {
	requestPath := strings.SplitN(raw, "?", 2)[0]
	requestPath = strings.TrimSuffix(requestPath, "/")
	for _, prefix := range []string{"/comfyui", "/openwebui"} {
		if strings.HasPrefix(requestPath, prefix) {
			requestPath = strings.TrimPrefix(requestPath, prefix)
			break
		}
	}
	return requestPath
}

func isStaticActivityPath(requestPath string) bool {
	ext := path.Ext(requestPath)
	switch ext {
	case ".js", ".css", ".map", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp", ".woff", ".woff2", ".ttf", ".html":
		return true
	default:
		return strings.Contains(requestPath, "/favicon")
	}
}

func isBackgroundActivityPath(requestPath string) bool {
	for _, marker := range []string{
		"/api/config",
		"/api/models",
		"/api/tags",
		"/api/object_info",
		"/api/system_stats",
		"/api/system",
		"/api/health",
		"/api/version",
	} {
		if strings.Contains(requestPath, marker) {
			return true
		}
	}
	return false
}
