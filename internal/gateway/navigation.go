package gateway

import (
	"context"
	"net/http"

	"ai-access-gateway/internal/domain"
)

const adminWorkspaceKey ctxKey = "admin_workspace"

func withAdminWorkspace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), adminWorkspaceKey, true)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type navigationItem struct {
	Label, Icon, Href string
	Task, External    bool
}

type navigationGroup struct {
	Label string
	Items []navigationItem
}

type navigationView struct {
	Groups        []navigationGroup
	Mobile        []navigationItem
	Home, Context string
}

type shellView struct {
	Data       map[string]any
	Navigation navigationView
	Admin      bool
}

func workspaceShell(data map[string]any, admin bool) shellView {
	user, _ := data["CurrentUser"].(*domain.User)
	adminWorkspace, _ := data["AdminWorkspace"].(bool)
	admin = (admin || adminWorkspace) && user != nil && user.Role == "admin"
	suggestions, _ := data["FeatureSuggestionsEnabled"].(bool)
	adminURL, _ := data["AdminBaseURL"].(string)
	publicURL, _ := data["PublicBaseURL"].(string)
	return shellView{Data: data, Admin: admin, Navigation: workspaceNavigation(user, admin, suggestions, adminURL, publicURL)}
}

// One permission-filtered inventory feeds the sidebar and the mobile shortcuts.
func workspaceNavigation(user *domain.User, admin, suggestions bool, adminURL, publicURL string) navigationView {
	view := navigationView{Home: "/app", Context: "Студия"}
	if user == nil {
		return view
	}
	if user.Role != "admin" && user.CanUseHarness && !user.CanUseQuickGeneration && !user.CanTrainImageLora {
		view.Home, view.Context = "/harness/", "Harness"
		entry := navigationItem{Label: "Harness", Icon: "bot", Href: "/harness/", External: true}
		view.Groups = []navigationGroup{{Items: []navigationItem{entry}}}
		view.Mobile = []navigationItem{entry}
		return view
	}
	item := func(label, icon, href string) navigationItem {
		return navigationItem{Label: label, Icon: icon, Href: href}
	}
	if admin && user.Role == "admin" {
		view.Home, view.Context = "/admin", "Управление"
		view.Groups = []navigationGroup{
			{Items: []navigationItem{
				item("Операции", "activity", "/admin"),
				item("Задачи и контент", "images", "/admin/content"),
				item("Harness Ultra", "image", "/admin/harness-ultra"),
				item("Пользователи", "users", "/admin/users"),
				item("Приглашения", "ticket", "/admin/invites"),
				item("Модели и workflow", "workflow", "/admin/workflows"),
				item("Модели и LoRA", "layers", "/admin/models"),
				item("Обучение LoRA", "scan-face", "/admin/lora-training"),
			}},
			{Label: "Инфраструктура", Items: []navigationItem{
				item("Сервисы", "server", "/admin/services/comfyui"),
				item("Майнинг", "cpu", "/admin/mining"),
				item("Хранилище", "hard-drive", "/admin/storage"),
				item("Обновления", "download", "/admin/updates"),
				item("Метрики", "chart-no-axes-combined", "/admin/metrics"),
				item("Журнал", "scroll-text", "/admin/audit"),
			}},
			{Label: "Дополнительно", Items: []navigationItem{
				item("Harness", "bot", publicURL+"/harness/"),
				item("Сессии", "monitor-smartphone", "/admin/sessions"),
				item("Предложения", "message-square", "/admin/suggestions"),
				item("Перейти в студию", "arrow-left", publicURL+"/generate"),
			}},
		}
		return view
	}
	primary := navigationGroup{}
	if user.Role == "admin" || user.CanUseQuickGeneration {
		view.Home = "/generate"
		primary.Items = append(primary.Items, item("Создать", "plus", "/generate"), item("Результаты", "images", "/gallery"))
	} else {
		primary.Items = append(primary.Items, item("Обзор", "layout-dashboard", "/app"))
	}
	if user.CanUseService("quick_generation") || user.CanTrainImageLora {
		primary.Items = append(primary.Items, item("Задачи", "list-checks", "/tasks"))
	}
	view.Mobile = append(view.Mobile, primary.Items...)
	primary.Items = append(primary.Items, item("Модели и LoRA", "layers", "/models"))
	if user.Role == "admin" || user.CanTrainImageLora {
		primary.Items = append(primary.Items, item("Мои LoRA", "scan-face", "/train-lora"))
	}
	view.Groups = append(view.Groups, primary)
	tools := navigationGroup{Label: "Инструменты"}
	if user.CanUseService("harness") {
		tools.Items = append(tools.Items, navigationItem{Label: "Harness", Icon: "bot", Href: "/harness/", External: true})
	}
	if len(tools.Items) > 0 {
		view.Groups = append(view.Groups, tools)
	}
	more := navigationGroup{Label: "Рабочее пространство"}
	if view.Home != "/app" {
		more.Items = append(more.Items, item("Обзор", "layout-dashboard", "/app"))
	}
	if suggestions {
		more.Items = append(more.Items, item("Предложения", "message-square", "/suggestions"))
	}
	if user.Role == "admin" {
		more.Items = append(more.Items, item("Управление", "settings-2", adminURL+"/admin"))
	}
	if len(more.Items) > 0 {
		view.Groups = append(view.Groups, more)
	}
	return view
}
