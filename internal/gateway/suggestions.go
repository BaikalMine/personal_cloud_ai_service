package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

const (
	maxSuggestionRequest = 6 << 20
	maxSuggestionJSON    = 5 << 20
	maxSuggestionLinks   = 3
)

type featureSuggestionView struct {
	domain.FeatureSuggestionRow
	Description string
	Links       []string
	JSONSize    int
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
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodPost {
		a.handleSuggestionSubmit(w, r)
		return
	}
	a.render(w, r, "suggestions", map[string]any{
		"Title": "Предложить улучшение", "VirusTotalConfigured": a.virusTotal != nil && a.virusTotal.Configured(),
	})
}

func (a *App) handleSuggestionSubmit(w http.ResponseWriter, r *http.Request) {
	if a.virusTotal == nil || !a.virusTotal.Configured() {
		a.renderStatus(w, r, http.StatusServiceUnavailable, "suggestions", map[string]any{
			"Title": "Предложить улучшение", "Error": "Приём предложений временно недоступен: администратор ещё не настроил проверку VirusTotal.",
		})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSuggestionRequest)
	if !a.validCSRF(r) {
		http.Error(w, "проверка безопасности не пройдена", http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(maxSuggestionJSON + (1 << 20)); err != nil {
		a.renderStatus(w, r, http.StatusBadRequest, "suggestions", map[string]any{"Title": "Предложить улучшение", "Error": "Не удалось прочитать форму или вложение слишком большое."})
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	description := strings.TrimSpace(r.FormValue("description"))
	if len([]rune(title)) < 3 || len([]rune(title)) > 120 {
		a.renderStatus(w, r, http.StatusBadRequest, "suggestions", map[string]any{"Title": "Предложить улучшение", "Error": "Название должно содержать от 3 до 120 символов."})
		return
	}
	if len([]rune(description)) < 10 || len([]rune(description)) > 4000 {
		a.renderStatus(w, r, http.StatusBadRequest, "suggestions", map[string]any{"Title": "Предложить улучшение", "Error": "Опишите предложение текстом от 10 до 4000 символов."})
		return
	}
	links, err := parseSuggestionLinks(r.FormValue("links"))
	if err != nil {
		a.renderStatus(w, r, http.StatusBadRequest, "suggestions", map[string]any{"Title": "Предложить улучшение", "Error": err.Error()})
		return
	}
	jsonName, jsonPayload, err := suggestionJSONAttachment(r)
	if err != nil {
		a.renderStatus(w, r, http.StatusBadRequest, "suggestions", map[string]any{"Title": "Предложить улучшение", "Error": err.Error()})
		return
	}
	if len(links) == 0 && len(jsonPayload) == 0 {
		a.renderStatus(w, r, http.StatusBadRequest, "suggestions", map[string]any{"Title": "Предложить улучшение", "Error": "Добавьте хотя бы одну ссылку или JSON-файл для проверки."})
		return
	}

	descriptionCipher, err := a.contentCipher.Encrypt(description)
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
	jsonCipher, err := a.contentCipher.EncryptBytes(jsonPayload)
	if err != nil {
		http.Error(w, "не удалось защитить JSON", http.StatusInternalServerError)
		return
	}
	scans := make([]domain.FeatureSuggestionScanRecord, 0, len(links)+1)
	for index := range links {
		scans = append(scans, domain.FeatureSuggestionScanRecord{Kind: "url", SourceName: fmt.Sprintf("Ссылка %d", index+1)})
	}
	if len(jsonPayload) > 0 {
		scans = append(scans, domain.FeatureSuggestionScanRecord{Kind: "json", SourceName: jsonName})
	}
	user := a.currentUser(r)
	suggestionID, createdScans, err := a.store.CreateFeatureSuggestion(r.Context(), domain.FeatureSuggestionRecord{
		UserID: user.ID, Username: user.Username, Title: title, DescriptionCipher: descriptionCipher, LinksCipher: linksCipher,
		JSONName: jsonName, JSONCipher: jsonCipher,
	}, scans)
	if err != nil {
		http.Error(w, "не удалось сохранить предложение", http.StatusInternalServerError)
		return
	}
	for index, scan := range createdScans {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		var scanErr error
		if scan.Kind == "url" {
			analysis, err := a.virusTotal.SubmitURL(ctx, links[index])
			if err == nil {
				scanErr = a.store.SetFeatureSuggestionScanSubmitted(r.Context(), scan.ID, analysis.ID, virusTotalStatus(analysis.Status))
			} else {
				scanErr = a.store.SetFeatureSuggestionScanError(r.Context(), scan.ID, truncate(err.Error(), 300))
			}
		} else {
			analysis, err := a.virusTotal.SubmitJSON(ctx, jsonName, jsonPayload)
			if err == nil {
				scanErr = a.store.SetFeatureSuggestionScanSubmitted(r.Context(), scan.ID, analysis.ID, virusTotalStatus(analysis.Status))
			} else {
				scanErr = a.store.SetFeatureSuggestionScanError(r.Context(), scan.ID, truncate(err.Error(), 300))
			}
		}
		cancel()
		if scanErr != nil {
			break
		}
	}
	_ = a.store.RefreshFeatureSuggestionStatus(r.Context(), suggestionID)
	a.audit(r.Context(), &user.ID, "feature_suggestion_created", "feature_suggestion", &suggestionID, a.clientIP(r), r.UserAgent(), map[string]any{"links": len(links), "json_attached": len(jsonPayload) > 0})
	a.render(w, r, "suggestions", map[string]any{
		"Title": "Предложить улучшение", "VirusTotalConfigured": true,
		"Message": "Предложение принято. Ссылки и JSON отправлены на проверку VirusTotal; результат увидит администратор.",
	})
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
	scans, err := a.store.PendingFeatureSuggestionScans(ctx, 24)
	if err != nil {
		return 0, err
	}
	var processed int64
	var scanErrors []error
	for _, scan := range scans {
		checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		analysis, checkErr := a.virusTotal.Analysis(checkCtx, scan.AnalysisID)
		cancel()
		suggestionID, idErr := a.store.FeatureSuggestionIDForScan(ctx, scan.ID)
		if idErr != nil {
			scanErrors = append(scanErrors, fmt.Errorf("scan %d suggestion: %w", scan.ID, idErr))
			continue
		}
		if checkErr != nil {
			if saveErr := a.store.SetFeatureSuggestionScanError(ctx, scan.ID, truncate(checkErr.Error(), 300)); saveErr != nil {
				scanErrors = append(scanErrors, fmt.Errorf("scan %d error state: %w", scan.ID, saveErr))
			}
			scanErrors = append(scanErrors, fmt.Errorf("scan %d VirusTotal: %w", scan.ID, checkErr))
		} else {
			if saveErr := a.store.SetFeatureSuggestionScanResult(ctx, scan.ID, virusTotalStatus(analysis.Status), analysis.Malicious, analysis.Suspicious, analysis.Harmless, analysis.Undetected, analysis.Timeout); saveErr != nil {
				scanErrors = append(scanErrors, fmt.Errorf("scan %d result: %w", scan.ID, saveErr))
			}
		}
		if refreshErr := a.store.RefreshFeatureSuggestionStatus(ctx, suggestionID); refreshErr != nil {
			scanErrors = append(scanErrors, fmt.Errorf("suggestion %d status: %w", suggestionID, refreshErr))
		}
		processed++
	}
	return processed, errors.Join(scanErrors...)
}
