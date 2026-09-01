package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

const (
	maxSuggestionRequest = 6 << 20
	maxSuggestionJSON    = 5 << 20
	maxSuggestionLinks   = 3
)

type featureSuggestionForm struct {
	ID          int64
	Kind        string
	Title       string
	Description string
	Links       string
	JSONName    string
	JSONSize    int64
}

type featureSuggestionLinkView struct {
	URL        string
	SourceName string
	Safe       bool
}

type featureSuggestionScanView struct {
	domain.FeatureSuggestionScan
	StatusLabel string
	StatusClass string
	Safe        bool
}

type featureSuggestionView struct {
	domain.FeatureSuggestionRow
	Description     string
	Links           []featureSuggestionLinkView
	ReviewComment   string
	KindLabel       string
	StatusLabel     string
	StatusClass     string
	StatusHint      string
	ScanStatusLabel string
	Scans           []featureSuggestionScanView
	AttachmentCount int
	CanEdit         bool
	CanRetry        bool
	CanAccept       bool
	CanReject       bool
	CanDownloadJSON bool
}

func (a *App) featureSuggestionsOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.cfg.FeatureSuggestionsEnabled {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleSuggestions(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/suggestions"), "/")
	switch {
	case path == "":
		if r.Method == http.MethodGet {
			a.handleSuggestionsPage(w, r)
			return
		}
		if r.Method == http.MethodPost {
			a.handleSuggestionWrite(w, r)
			return
		}
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
	case strings.HasSuffix(path, "/delete"):
		a.handleSuggestionDelete(w, r, strings.TrimSuffix(path, "/delete"))
	default:
		http.NotFound(w, r)
	}
}

func (a *App) handleSuggestionsPage(w http.ResponseWriter, r *http.Request) {
	form := featureSuggestionForm{Kind: "other"}
	if rawID := strings.TrimSpace(r.URL.Query().Get("edit")); rawID != "" {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id <= 0 {
			http.NotFound(w, r)
			return
		}
		row, err := a.store.FeatureSuggestionByIDForUser(r.Context(), id, a.currentUser(r).ID)
		if err != nil || row.Status != "draft" {
			http.NotFound(w, r)
			return
		}
		form, err = a.featureSuggestionFormFromRow(row)
		if err != nil {
			http.Error(w, "не удалось открыть черновик", http.StatusInternalServerError)
			return
		}
	}
	a.renderSuggestionsPage(w, r, http.StatusOK, form, "", suggestionMessage(r.URL.Query().Get("message")))
}

func (a *App) renderSuggestionsPage(w http.ResponseWriter, r *http.Request, status int, form featureSuggestionForm, errorMessage, message string) {
	rows, err := a.store.ListFeatureSuggestionsByUser(r.Context(), a.currentUser(r).ID, 50)
	if err != nil {
		http.Error(w, "не удалось загрузить предложения", http.StatusInternalServerError)
		return
	}
	items := make([]featureSuggestionView, 0, len(rows))
	for _, row := range rows {
		item, decodeErr := a.featureSuggestionView(row)
		if decodeErr != nil {
			http.Error(w, "не удалось расшифровать предложение", http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	if form.Kind == "" {
		form.Kind = "other"
	}
	a.renderStatus(w, r, status, "suggestions", map[string]any{
		"Title": "Предложения", "Form": form, "Suggestions": items, "Error": errorMessage, "Message": message,
		"VirusTotalConfigured": a.virusTotal != nil && a.virusTotal.Configured(),
	})
}

func (a *App) handleSuggestionWrite(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSuggestionRequest)
	if err := r.ParseMultipartForm(maxSuggestionJSON + (256 << 10)); err != nil {
		a.renderSuggestionsPage(w, r, http.StatusBadRequest, featureSuggestionForm{Kind: "other"}, "Не удалось прочитать форму или вложение слишком большое.", "")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if !a.validCSRF(r) {
		http.Error(w, "проверка безопасности не пройдена", http.StatusForbidden)
		return
	}
	action := strings.TrimSpace(r.FormValue("action"))
	if action != "save" && action != "submit" {
		http.Error(w, "неизвестное действие", http.StatusBadRequest)
		return
	}
	form := featureSuggestionForm{
		Kind: strings.TrimSpace(r.FormValue("kind")), Title: strings.TrimSpace(r.FormValue("title")),
		Description: strings.TrimSpace(r.FormValue("description")), Links: strings.TrimSpace(r.FormValue("links")),
	}
	if rawID := strings.TrimSpace(r.FormValue("suggestion_id")); rawID != "" {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id <= 0 {
			a.renderSuggestionsPage(w, r, http.StatusBadRequest, form, "Некорректный идентификатор черновика.", "")
			return
		}
		form.ID = id
	}
	if !validSuggestionKind(form.Kind) {
		a.renderSuggestionsPage(w, r, http.StatusBadRequest, form, "Выберите тип предложения.", "")
		return
	}
	if len([]rune(form.Title)) < 3 || len([]rune(form.Title)) > 120 {
		a.renderSuggestionsPage(w, r, http.StatusBadRequest, form, "Название должно содержать от 3 до 120 символов.", "")
		return
	}
	descriptionLength := len([]rune(form.Description))
	if descriptionLength > 4000 || (action == "submit" && descriptionLength < 10) {
		errorMessage := "Описание не должно превышать 4000 символов."
		if action == "submit" {
			errorMessage = "Перед отправкой опишите предложение текстом от 10 до 4000 символов."
		}
		a.renderSuggestionsPage(w, r, http.StatusBadRequest, form, errorMessage, "")
		return
	}
	links, err := parseSuggestionLinks(form.Links)
	if err != nil {
		a.renderSuggestionsPage(w, r, http.StatusBadRequest, form, err.Error(), "")
		return
	}

	user := a.currentUser(r)
	var existing domain.FeatureSuggestionRow
	if form.ID > 0 {
		existing, err = a.store.FeatureSuggestionByIDForUser(r.Context(), form.ID, user.ID)
		if err != nil || existing.Status != "draft" {
			http.NotFound(w, r)
			return
		}
	}
	jsonName, jsonPayload, err := suggestionJSONAttachment(r)
	if err != nil {
		a.renderSuggestionsPage(w, r, http.StatusBadRequest, form, err.Error(), "")
		return
	}
	jsonCipher := existing.JSONCipher
	jsonSize := existing.JSONSizeBytes
	if r.FormValue("remove_json") == "1" {
		jsonName, jsonPayload, jsonCipher, jsonSize = "", nil, nil, 0
	} else if len(jsonPayload) == 0 && existing.JSONName != "" {
		jsonName = existing.JSONName
		form.JSONName, form.JSONSize = existing.JSONName, existing.JSONSizeBytes
	}

	descriptionCipher, err := a.contentCipher.Encrypt(form.Description)
	if err != nil {
		http.Error(w, "не удалось защитить описание", http.StatusInternalServerError)
		return
	}
	linksJSON, _ := json.Marshal(links)
	linksCipher, err := a.contentCipher.Encrypt(string(linksJSON))
	if err != nil {
		http.Error(w, "не удалось защитить ссылки", http.StatusInternalServerError)
		return
	}
	if jsonCipher == nil || len(jsonPayload) > 0 {
		jsonCipher, err = a.contentCipher.EncryptBytes(jsonPayload)
		if err != nil {
			http.Error(w, "не удалось защитить JSON", http.StatusInternalServerError)
			return
		}
		jsonSize = int64(len(jsonPayload))
	}
	form.JSONName, form.JSONSize = jsonName, jsonSize
	record := domain.FeatureSuggestionRecord{
		UserID: user.ID, Username: user.Username, Kind: form.Kind, Title: form.Title,
		DescriptionCipher: descriptionCipher, LinksCipher: linksCipher, JSONName: jsonName,
		JSONCipher: jsonCipher, JSONSizeBytes: jsonSize,
	}
	var draft domain.FeatureSuggestionRow
	if form.ID == 0 {
		draft, err = a.store.CreateFeatureSuggestionDraft(r.Context(), record)
	} else {
		draft, err = a.store.UpdateFeatureSuggestionDraft(r.Context(), form.ID, user.ID, record)
	}
	if err != nil {
		http.Error(w, "не удалось сохранить предложение", http.StatusInternalServerError)
		return
	}
	form.ID = draft.ID
	if action == "save" {
		a.audit(r.Context(), &user.ID, "feature_suggestion_draft_saved", "feature_suggestion", &draft.ID, a.clientIP(r), r.UserAgent(), nil)
		http.Redirect(w, r, fmt.Sprintf("/suggestions?edit=%d&message=saved", draft.ID), http.StatusSeeOther)
		return
	}

	scans := suggestionScanRecords(links, jsonName)
	if len(scans) > 0 && (a.virusTotal == nil || !a.virusTotal.Configured()) {
		a.renderSuggestionsPage(w, r, http.StatusServiceUnavailable, form, "Черновик сохранён. Сейчас VirusTotal недоступен, поэтому предложение с вложениями пока нельзя отправить.", "")
		return
	}
	if _, _, err := a.store.SubmitFeatureSuggestion(r.Context(), draft.ID, user.ID, scans); err != nil {
		http.Error(w, "не удалось отправить предложение", http.StatusInternalServerError)
		return
	}
	a.audit(r.Context(), &user.ID, "feature_suggestion_submitted", "feature_suggestion", &draft.ID, a.clientIP(r), r.UserAgent(), map[string]any{"kind": form.Kind, "links": len(links), "json_attached": jsonName != ""})
	http.Redirect(w, r, "/suggestions?message=submitted", http.StatusSeeOther)
}

func (a *App) handleSuggestionDelete(w http.ResponseWriter, r *http.Request, rawID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "проверка безопасности не пройдена", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(strings.Trim(rawID, "/"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	user := a.currentUser(r)
	deleted, err := a.store.DeleteFeatureSuggestionDraft(r.Context(), id, user.ID)
	if err != nil {
		http.Error(w, "не удалось удалить черновик", http.StatusInternalServerError)
		return
	}
	if !deleted {
		http.NotFound(w, r)
		return
	}
	a.audit(r.Context(), &user.ID, "feature_suggestion_draft_deleted", "feature_suggestion", &id, a.clientIP(r), r.UserAgent(), nil)
	http.Redirect(w, r, "/suggestions?message=deleted", http.StatusSeeOther)
}

func (a *App) featureSuggestionFormFromRow(row domain.FeatureSuggestionRow) (featureSuggestionForm, error) {
	description, err := a.contentCipher.Decrypt(row.DescriptionCipher)
	if err != nil {
		return featureSuggestionForm{}, err
	}
	linksRaw, err := a.contentCipher.Decrypt(row.LinksCipher)
	if err != nil {
		return featureSuggestionForm{}, err
	}
	var links []string
	if err := json.Unmarshal([]byte(linksRaw), &links); err != nil {
		return featureSuggestionForm{}, err
	}
	return featureSuggestionForm{ID: row.ID, Kind: row.Kind, Title: row.Title, Description: description, Links: strings.Join(links, "\n"), JSONName: row.JSONName, JSONSize: row.JSONSizeBytes}, nil
}

func (a *App) featureSuggestionView(row domain.FeatureSuggestionRow) (featureSuggestionView, error) {
	description, err := a.contentCipher.Decrypt(row.DescriptionCipher)
	if err != nil {
		return featureSuggestionView{}, err
	}
	linksRaw, err := a.contentCipher.Decrypt(row.LinksCipher)
	if err != nil {
		return featureSuggestionView{}, err
	}
	var links []string
	if err := json.Unmarshal([]byte(linksRaw), &links); err != nil {
		return featureSuggestionView{}, err
	}
	statusLabel, statusClass, statusHint := featureSuggestionStatus(row.Status)
	item := featureSuggestionView{
		FeatureSuggestionRow: row, Description: description, KindLabel: featureSuggestionKindLabel(row.Kind),
		StatusLabel: statusLabel, StatusClass: statusClass, StatusHint: statusHint,
		ScanStatusLabel: featureSuggestionScanStatusLabel(row.ScanStatus), AttachmentCount: len(row.Scans),
		CanEdit: row.Status == "draft", CanRetry: row.Status == "review" && len(row.Scans) > 0,
		CanAccept: row.Status == "review" && (row.ScanStatus == "none" || row.ScanStatus == "clean"),
		CanReject: row.Status == "review",
	}
	if len(row.ReviewCommentCipher) > 0 {
		item.ReviewComment, err = a.contentCipher.Decrypt(row.ReviewCommentCipher)
		if err != nil {
			return featureSuggestionView{}, err
		}
	}
	for index, target := range links {
		scan, found := featureSuggestionScanAt(row.Scans, "url", index)
		link := featureSuggestionLinkView{URL: target, SourceName: fmt.Sprintf("Ссылка %d", index+1)}
		if found {
			link.SourceName = scan.SourceName
			link.Safe = featureSuggestionScanSafe(scan)
		}
		item.Links = append(item.Links, link)
	}
	for _, scan := range row.Scans {
		label, class := featureSuggestionScanStatus(scan)
		item.Scans = append(item.Scans, featureSuggestionScanView{FeatureSuggestionScan: scan, StatusLabel: label, StatusClass: class, Safe: featureSuggestionScanSafe(scan)})
		if scan.Kind == "json" && featureSuggestionScanSafe(scan) && (row.Status == "review" || row.Status == "accepted") {
			item.CanDownloadJSON = true
		}
	}
	return item, nil
}

func suggestionScanRecords(links []string, jsonName string) []domain.FeatureSuggestionScanRecord {
	items := make([]domain.FeatureSuggestionScanRecord, 0, len(links)+1)
	for index := range links {
		items = append(items, domain.FeatureSuggestionScanRecord{Kind: "url", SourceName: fmt.Sprintf("Ссылка %d", index+1), SourceIndex: index})
	}
	if jsonName != "" {
		items = append(items, domain.FeatureSuggestionScanRecord{Kind: "json", SourceName: jsonName, SourceIndex: 0})
	}
	return items
}

func validSuggestionKind(value string) bool {
	switch value {
	case "lora", "model", "workflow", "other":
		return true
	default:
		return false
	}
}

func featureSuggestionKindLabel(value string) string {
	switch value {
	case "lora":
		return "LoRA"
	case "model":
		return "Модель"
	case "workflow":
		return "Workflow"
	default:
		return "Другое"
	}
}

func featureSuggestionStatus(value string) (string, string, string) {
	switch value {
	case "draft":
		return "Черновик", "neutral", "Можно продолжить редактирование и отправить позже."
	case "submitted":
		return "Отправлено", "info", "Вложения ожидают безопасной проверки."
	case "scanning":
		return "Проверяется", "info", "Проверяем приложенные ссылки и JSON."
	case "review":
		return "На рассмотрении", "warning", "Проверка завершена, решение принимает администратор."
	case "accepted":
		return "Принято", "ok", "Предложение принято в работу без автоматической установки."
	case "rejected":
		return "Отклонено", "danger", "Администратор завершил рассмотрение."
	default:
		return "Неизвестно", "neutral", "Статус обновляется."
	}
}

func featureSuggestionScanStatusLabel(value string) string {
	switch value {
	case "none":
		return "Без вложений"
	case "queued":
		return "Ожидает проверки"
	case "scanning":
		return "Проверяется"
	case "clean":
		return "Проверено"
	case "flagged":
		return "Найдены риски"
	case "error":
		return "Ошибка проверки"
	default:
		return "Неизвестно"
	}
}

func featureSuggestionScanStatus(scan domain.FeatureSuggestionScan) (string, string) {
	if scan.Malicious > 0 || scan.Suspicious > 0 {
		return "Найдены риски", "danger"
	}
	switch scan.Status {
	case "completed":
		return "Проверено", "ok"
	case "error":
		return "Ошибка", "danger"
	case "in-progress":
		return "Проверяется", "info"
	default:
		return "В очереди", "neutral"
	}
}

func featureSuggestionScanSafe(scan domain.FeatureSuggestionScan) bool {
	return scan.Status == "completed" && scan.Malicious == 0 && scan.Suspicious == 0
}

func featureSuggestionScanAt(scans []domain.FeatureSuggestionScan, kind string, index int) (domain.FeatureSuggestionScan, bool) {
	for _, scan := range scans {
		if scan.Kind == kind && scan.SourceIndex == index {
			return scan, true
		}
	}
	return domain.FeatureSuggestionScan{}, false
}

func suggestionMessage(code string) string {
	switch code {
	case "saved":
		return "Черновик сохранён."
	case "submitted":
		return "Предложение отправлено. Статус будет обновляться здесь."
	case "deleted":
		return "Черновик удалён."
	default:
		return ""
	}
}

func parseSuggestionLinks(raw string) ([]string, error) {
	lines := strings.FieldsFunc(raw, func(char rune) bool { return char == '\n' || char == '\r' || char == ',' })
	if len(lines) > maxSuggestionLinks {
		return nil, fmt.Errorf("можно добавить не более %d ссылок", maxSuggestionLinks)
	}
	links := make([]string, 0, len(lines))
	seen := make(map[string]struct{})
	for _, value := range lines {
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return nil, errors.New("каждая ссылка должна быть обычным адресом http или https без учётных данных")
		}
		if suggestionPrivateHost(parsed.Hostname()) {
			return nil, errors.New("локальные и внутренние адреса нельзя отправлять на проверку")
		}
		canonical := parsed.String()
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		links = append(links, canonical)
	}
	return links, nil
}

func suggestionPrivateHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	if address := net.ParseIP(host); address != nil {
		return address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsUnspecified()
	}
	return false
}

func suggestionJSONAttachment(r *http.Request) (string, []byte, error) {
	file, header, err := r.FormFile("workflow_json")
	if errors.Is(err, http.ErrMissingFile) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, errors.New("не удалось прочитать JSON-файл")
	}
	defer file.Close()
	name := filepath.Base(strings.TrimSpace(header.Filename))
	if name == "." || name == "" || !strings.EqualFold(filepath.Ext(name), ".json") || len(name) > 180 {
		return "", nil, errors.New("можно приложить только JSON-файл с корректным именем")
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxSuggestionJSON+1))
	if err != nil || len(payload) == 0 || len(payload) > maxSuggestionJSON {
		return "", nil, fmt.Errorf("JSON-файл должен быть не пустым и не больше %d МБ", maxSuggestionJSON>>20)
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return "", nil, errors.New("вложение должно быть корректным JSON, оно не выполняется Gateway")
	}
	return name, payload, nil
}

func virusTotalStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "completed":
		return "completed"
	case "in-progress", "in_progress":
		return "in-progress"
	default:
		return "queued"
	}
}

func (a *App) refreshFeatureSuggestionScans(ctx context.Context) (int64, error) {
	if a.virusTotal == nil || !a.virusTotal.Configured() {
		return 0, nil
	}
	scans, err := a.store.ClaimFeatureSuggestionScans(ctx, 4, 3*time.Minute)
	if err != nil {
		return 0, err
	}
	var processed int64
	var scanErrors []error
	for _, scan := range scans {
		suggestion, loadErr := a.store.FeatureSuggestionByID(ctx, scan.SuggestionID)
		if loadErr != nil {
			if saveErr := a.store.SetFeatureSuggestionScanError(ctx, scan.ID, scan.LeaseToken, truncate(loadErr.Error(), 300), false); saveErr != nil {
				scanErrors = append(scanErrors, saveErr)
			}
			scanErrors = append(scanErrors, fmt.Errorf("scan %d source: %w", scan.ID, loadErr))
			if refreshErr := a.store.RefreshFeatureSuggestionStatus(ctx, scan.SuggestionID); refreshErr != nil {
				scanErrors = append(scanErrors, fmt.Errorf("suggestion %d status: %w", scan.SuggestionID, refreshErr))
			}
			processed++
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		analysisID := scan.AnalysisID
		status := scan.Status
		malicious, suspicious, harmless, undetected, timeout := scan.Malicious, scan.Suspicious, scan.Harmless, scan.Undetected, scan.Timeout
		submitted := analysisID == ""
		var scanErr error
		if submitted {
			if scan.Kind == "url" {
				var target string
				target, scanErr = a.featureSuggestionURL(suggestion, scan.SourceIndex)
				if scanErr == nil {
					analysis, submitErr := a.virusTotal.SubmitURL(checkCtx, target)
					scanErr = submitErr
					analysisID, status = analysis.ID, virusTotalStatus(analysis.Status)
					malicious, suspicious, harmless, undetected, timeout = analysis.Malicious, analysis.Suspicious, analysis.Harmless, analysis.Undetected, analysis.Timeout
				}
			} else {
				var payload []byte
				payload, scanErr = a.featureSuggestionJSON(suggestion, scan.SourceIndex)
				if scanErr == nil {
					analysis, submitErr := a.virusTotal.SubmitJSON(checkCtx, suggestion.JSONName, payload)
					scanErr = submitErr
					analysisID, status = analysis.ID, virusTotalStatus(analysis.Status)
					malicious, suspicious, harmless, undetected, timeout = analysis.Malicious, analysis.Suspicious, analysis.Harmless, analysis.Undetected, analysis.Timeout
				}
			}
		} else {
			analysis, analysisErr := a.virusTotal.Analysis(checkCtx, analysisID)
			scanErr = analysisErr
			if analysisErr == nil {
				analysisID, status = analysis.ID, virusTotalStatus(analysis.Status)
				malicious, suspicious, harmless, undetected, timeout = analysis.Malicious, analysis.Suspicious, analysis.Harmless, analysis.Undetected, analysis.Timeout
			}
		}
		cancel()
		if scanErr != nil {
			if saveErr := a.store.SetFeatureSuggestionScanError(ctx, scan.ID, scan.LeaseToken, truncate(scanErr.Error(), 300), submitted); saveErr != nil {
				scanErrors = append(scanErrors, fmt.Errorf("scan %d error state: %w", scan.ID, saveErr))
			}
			scanErrors = append(scanErrors, fmt.Errorf("scan %d VirusTotal: %w", scan.ID, scanErr))
		} else if saveErr := a.store.SetFeatureSuggestionScanResult(ctx, scan.ID, scan.LeaseToken, analysisID, status, malicious, suspicious, harmless, undetected, timeout, submitted); saveErr != nil {
			scanErrors = append(scanErrors, fmt.Errorf("scan %d result: %w", scan.ID, saveErr))
		}
		if refreshErr := a.store.RefreshFeatureSuggestionStatus(ctx, scan.SuggestionID); refreshErr != nil {
			scanErrors = append(scanErrors, fmt.Errorf("suggestion %d status: %w", scan.SuggestionID, refreshErr))
		}
		processed++
	}
	return processed, errors.Join(scanErrors...)
}

func (a *App) featureSuggestionURL(suggestion domain.FeatureSuggestionRow, sourceIndex int) (string, error) {
	raw, err := a.contentCipher.Decrypt(suggestion.LinksCipher)
	if err != nil {
		return "", err
	}
	var links []string
	if err := json.Unmarshal([]byte(raw), &links); err != nil {
		return "", err
	}
	if sourceIndex < 0 || sourceIndex >= len(links) {
		return "", sql.ErrNoRows
	}
	return links[sourceIndex], nil
}

func (a *App) featureSuggestionJSON(suggestion domain.FeatureSuggestionRow, sourceIndex int) ([]byte, error) {
	if sourceIndex != 0 || suggestion.JSONName == "" || suggestion.JSONSizeBytes <= 0 {
		return nil, sql.ErrNoRows
	}
	payload, err := a.contentCipher.DecryptBytes(suggestion.JSONCipher)
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) != suggestion.JSONSizeBytes || !json.Valid(payload) {
		return nil, errors.New("сохранённый JSON повреждён")
	}
	return payload, nil
}
