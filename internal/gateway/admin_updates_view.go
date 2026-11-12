package gateway

import (
	"time"

	"ai-access-gateway/internal/updates"
)

type adminUpdateComponent struct {
	updates.ComponentStatus
	Label, Tone, Hint, Icon string
	Selected, CanCheck      bool
	InstallAllowed          bool
}

type adminUpdateResult struct {
	Label, Tone, Message string
	Targets              []string
	At                   time.Time
}

type adminUpdatesView struct {
	At                   time.Time
	Available            bool
	AgentLabel           string
	Components           []adminUpdateComponent
	Overview             UpdateOverview
	CanCheck, CanInstall bool
	Result               adminUpdateResult
}

func updateComponentName(name string) string {
	switch name {
	case updates.ComponentGateway:
		return "Gateway"
	case updates.ComponentComfyUI:
		return "ComfyUI"
	case updates.ComponentOpenWebUI:
		return "OpenWebUI"
	default:
		return "Неизвестный компонент"
	}
}

func updateComponentHint(message string) string {
	switch message {
	case "В ComfyUI есть активная очередь: обновление отложено.":
		return "В ComfyUI есть активные задачи. Дождитесь завершения очереди."
	case "Есть локальные изменения: автоматическое обновление заблокировано.", "Есть локальные изменения ComfyUI: автоматическое обновление заблокировано.":
		return "Есть локальные изменения исходников. Проверьте их перед обновлением."
	case "Файл Docker Compose Open WebUI недоступен.":
		return "Агенту недоступна конфигурация OpenWebUI."
	default:
		return "Причина указана в диагностике компонента."
	}
}

func newAdminUpdatesView(now time.Time, status updates.Status, selected []string, action string, commandErr error) adminUpdatesView {
	v := adminUpdatesView{At: now, Available: status.Available && commandErr == nil, AgentLabel: "Нет подтвержденного ответа"}
	if v.Available {
		v.AgentLabel = "Ответ получен"
	}
	seen := map[string]bool{}
	for _, item := range status.Components {
		if !updates.ValidComponent(item.Name) || item.Name == updates.ComponentOpenWebUI || seen[item.Name] {
			continue
		}
		seen[item.Name] = true
		row := adminUpdateComponent{ComponentStatus: item, Label: "Не проверен", Tone: "warning", Icon: "boxes", Selected: hasString(selected, item.Name)}
		if row.DisplayName == "" {
			row.DisplayName = updateComponentName(item.Name)
		}
		if item.Name == updates.ComponentComfyUI {
			row.Icon = "workflow"
		} else if item.Name == updates.ComponentOpenWebUI {
			row.Icon = "messages-square"
		}
		fresh := !item.CheckedAt.IsZero() && !item.CheckedAt.After(now.Add(5*time.Second)) && now.Sub(item.CheckedAt) <= 15*time.Minute
		row.CanCheck = v.Available && item.Configured
		switch {
		case !item.Configured:
			row.Label, row.Hint = "Не настроен", "Проверьте настройки компонента в агенте обновлений."
		case !v.Available:
			row.Label, row.Hint = "Не подтверждено", "Обновите состояние после восстановления связи с агентом."
		case !fresh:
			row.Label, row.Hint = "Требуется проверка", "Получите свежие сведения перед установкой."
		case item.Message != "":
			row.Label, row.Hint = "Требует внимания", updateComponentHint(item.Message)
		case item.CurrentVersion == "" || item.LatestVersion == "":
			row.Label, row.Hint = "Версия неизвестна", "Не удалось подтвердить обе версии. Повторите проверку."
		case item.UpdateAvailable:
			row.Label, row.Tone = "Есть новая версия", "info"
			if item.CanInstall {
				row.InstallAllowed = true
				row.Hint = "Установка повторно проверит компонент и перезапустит его при обновлении."
			} else {
				row.Hint = "Агент пока не разрешает установку. Повторите проверку."
			}
		default:
			row.Label, row.Tone = "Версии совпадают", "success"
		}
		switch {
		case row.InstallAllowed:
			v.Overview.Available++
		case row.Tone == "success":
			v.Overview.Current++
		default:
			v.Overview.Blocked++
		}
		v.CanCheck = v.CanCheck || row.CanCheck
		v.CanInstall = v.CanInstall || row.InstallAllowed
		v.Components = append(v.Components, row)
	}
	if action != "" || commandErr != nil || status.Message != "" {
		v.Result = adminUpdateResult{At: now, Message: status.Message, Tone: "info", Label: "Сведения агента"}
		if action != "" {
			for _, name := range selected {
				v.Result.Targets = append(v.Result.Targets, updateComponentName(name))
			}
		}
		switch {
		case action == "install" && commandErr != nil:
			v.Result.Label, v.Result.Tone = "Результат установки не подтвержден", "warning"
		case commandErr != nil:
			v.Result.Label, v.Result.Tone = "Не удалось завершить проверку", "warning"
		case action == "install":
			v.Result.Label = "Обработка команды завершена"
		case action == "check":
			v.Result.Label = "Проверка завершена"
		}
	}
	return v
}
