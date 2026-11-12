package gateway

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestSuggestionsWorkspaceIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name, marker string
	if err := db.QueryRowContext(ctx, `SELECT current_database(),COALESCE(current_setting('ai_gateway.disposable_test',true),'')`).Scan(&name, &marker); err != nil {
		t.Fatal(err)
	}
	// This fixture may clear data only in its explicitly marked, disposable database.
	if name != "suggestions_d09_ephemeral" || marker != "suggestions-d09-20260914" {
		t.Skip("requires marked disposable suggestions database")
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	defer resetGatewayIntegrationDatabase(t, db)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password_hash,role) VALUES (1,'suggestion-admin','hash','admin'),(2,'suggestion-author','hash','user'),(3,'another-user','hash','user')`); err != nil {
		t.Fatal(err)
	}
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := contentcrypto.NewCipher("suggestions-test-only")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: store.New(db), tpl: tpl, contentCipher: cipher, csrfSigner: security.NewCSRFSigner("suggestions-test-only")}
	token := "suggestions-test-token"
	csrf := a.csrfSigner.Token(token)
	users := map[int64]*User{1: {ID: 1, Username: "suggestion-admin", Role: "admin"}, 2: {ID: 2, Username: "suggestion-author", Role: "user"}, 3: {ID: 3, Username: "another-user", Role: "user"}}
	request := func(method, target string, userID int64, values url.Values, jsonFile bool) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		contentType := "application/x-www-form-urlencoded"
		if method == "POST" && target == "/suggestions" {
			mw := multipart.NewWriter(&body)
			for key, entries := range values {
				for _, value := range entries {
					if err := mw.WriteField(key, value); err != nil {
						t.Fatal(err)
					}
				}
			}
			if jsonFile {
				part, err := mw.CreateFormFile("workflow_json", "workflow.json")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := part.Write([]byte(`{"version":1}`)); err != nil {
					t.Fatal(err)
				}
			}
			if err := mw.Close(); err != nil {
				t.Fatal(err)
			}
			contentType = mw.FormDataContentType()
		} else {
			body.WriteString(values.Encode())
		}
		r := httptest.NewRequest(method, target, &body)
		r.Header.Set("Content-Type", contentType)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		if user := users[userID]; user != nil {
			r = r.WithContext(context.WithValue(ctx, userCtxKey, user))
		}
		w := httptest.NewRecorder()
		if strings.HasPrefix(target, "/admin/") {
			a.requireAdmin(http.HandlerFunc(a.handleAdminRoutes)).ServeHTTP(w, r)
		} else {
			a.requireAuth(a.featureSuggestionsOnly(http.HandlerFunc(a.handleSuggestions))).ServeHTTP(w, r)
		}
		return w
	}
	wantCode := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
		}
	}
	for _, uid := range []int64{1, 2, 3} {
		wantCode(request("GET", "/suggestions", uid, nil, false), 404)
	}
	wantCode(request("GET", "/admin/suggestions", 2, nil, false), 403)
	wantCode(request("GET", "/admin/suggestions", 0, nil, false), 302)
	wantCode(request("GET", "/admin/suggestions", 1, nil, false), 200)
	a.cfg.FeatureSuggestionsEnabled = true
	fields := url.Values{"csrf": {csrf}, "kind": {"workflow"}, "title": {"Новый workflow"}, "description": {"Подробное описание нового workflow"}, "action": {"save"}}
	w := request("POST", "/suggestions", 2, fields, true)
	wantCode(w, 303)
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := strconv.ParseInt(location.Query().Get("edit"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	row, err := a.store.FeatureSuggestionByID(ctx, id)
	if err != nil || row.Status != "draft" {
		t.Fatal(row, err)
	}
	view, err := a.featureSuggestionView(row)
	if err != nil || view.AttachmentCount != 1 || view.CanDownloadJSON {
		t.Fatal(view, err)
	}
	fields.Set("suggestion_id", strconv.FormatInt(id, 10))
	fields.Set("title", "x")
	w = request("POST", "/suggestions", 2, fields, false)
	wantCode(w, 400)
	if !strings.Contains(w.Body.String(), "workflow.json") || !strings.Contains(w.Body.String(), "Подробное описание") {
		t.Fatal("draft content or saved attachment lost", w.Body.String())
	}
	fields.Set("title", "Новый workflow")
	fields.Set("action", "submit")
	wantCode(request("POST", "/suggestions", 2, fields, false), 503)
	row, err = a.store.FeatureSuggestionByID(ctx, id)
	if err != nil || row.Status != "draft" {
		t.Fatal("unavailable scanning must retain draft", row, err)
	}
	if _, _, err := a.store.SubmitFeatureSuggestion(ctx, id, 2, suggestionScanRecords(nil, "workflow.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE feature_suggestion_scans SET status='completed',malicious=1 WHERE suggestion_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := a.store.RefreshFeatureSuggestionStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	actionURL := fmt.Sprintf("/admin/suggestions/%d/decision", id)
	decision := url.Values{"csrf": {csrf}, "decision": {"accepted"}, "comment": {""}}
	w = request("POST", actionURL, 1, decision, false)
	wantCode(w, 303)
	if !strings.Contains(w.Header().Get("Location"), "error=unsafe") {
		t.Fatal("unsafe acceptance", w.Header())
	}
	wantCode(request("GET", fmt.Sprintf("/admin/suggestions/%d/json", id), 1, nil, false), 404)
	if _, err := db.ExecContext(ctx, `UPDATE feature_suggestion_scans SET malicious=0 WHERE suggestion_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := a.store.RefreshFeatureSuggestionStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	w = request("GET", fmt.Sprintf("/admin/suggestions/%d/json", id), 1, nil, false)
	wantCode(w, 200)
	if w.Body.String() != `{"version":1}` || !strings.Contains(w.Header().Get("Content-Disposition"), `attachment; filename="workflow.json"`) || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("invalid JSON download contract", w.Header(), w.Body.String())
	}
	if _, err := db.ExecContext(ctx, `UPDATE feature_suggestion_scans SET malicious=1 WHERE suggestion_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := a.store.RefreshFeatureSuggestionStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	decision.Set("decision", "rejected")
	decision.Set("comment", "Не")
	w = request("POST", actionURL, 1, decision, false)
	wantCode(w, 400)
	if !strings.Contains(w.Body.String(), ">Не</textarea>") || !strings.Contains(w.Body.String(), "</html>") {
		t.Fatal("comment lost or partial render", w.Body.String())
	}
	decision.Set("comment", "Проверка обнаружила угрозу.")
	wantCode(request("POST", actionURL, 2, decision, false), 403)
	decision.Set("csrf", "invalid")
	wantCode(request("POST", actionURL, 1, decision, false), 403)
	decision.Set("csrf", csrf)
	decision["decision"] = []string{"rejected", "accepted"}
	wantCode(request("POST", actionURL, 1, decision, false), 400)
	decision.Set("decision", "rejected")
	wantCode(request("POST", actionURL, 1, decision, false), 303)
	wantCode(request("POST", actionURL, 1, decision, false), 303)
	var decisions int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE action='feature_suggestion_rejected' AND target_id=$1`, id).Scan(&decisions); err != nil || decisions != 1 {
		t.Fatal("duplicate decision", decisions, err)
	}
	fields.Del("suggestion_id")
	fields.Set("action", "save")
	w = request("POST", "/suggestions", 2, fields, false)
	wantCode(w, 303)
	location, _ = url.Parse(w.Header().Get("Location"))
	deleteURL := "/suggestions/" + location.Query().Get("edit") + "/delete"
	confirm := url.Values{"csrf": {csrf}}
	wantCode(request("POST", deleteURL, 2, confirm, false), 400)
	confirm.Set("confirm_delete", "1")
	wantCode(request("POST", deleteURL, 3, confirm, false), 404)
	wantCode(request("GET", deleteURL, 2, confirm, false), 405)
	wantCode(request("POST", deleteURL, 2, confirm, false), 303)
	w = request("GET", "/admin/suggestions", 1, nil, false)
	wantCode(w, 200)
	if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "</html>") {
		t.Fatal("incomplete admin page", w.Header(), w.Body.String())
	}
	deadDB, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := deadDB.Close(); err != nil {
		t.Fatal(err)
	}
	a.store = store.New(deadDB)
	w = request("GET", "/admin/suggestions", 1, nil, false)
	wantCode(w, 503)
	if !strings.Contains(w.Body.String(), "Предложения не загружены") || strings.Contains(w.Body.String(), "Предложений пока нет") {
		t.Fatal("database failure shown as empty queue", w.Body.String())
	}
	w = request("POST", actionURL, 1, decision, false)
	wantCode(w, 503)
	if !strings.Contains(w.Body.String(), "Результат сохранения не подтверждён") || strings.Contains(w.Body.String(), `name="decision"`) {
		t.Fatal("unknown write permits replay", w.Body.String())
	}
	decision.Set("comment", strings.Repeat("x", 65<<10))
	wantCode(request("POST", actionURL, 1, decision, false), 400)
}
