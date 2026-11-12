package gateway

import (
	"ai-access-gateway/internal/mining"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestMiningBrowserFoldersFavoritesAndMissingFiles(t *testing.T) {
	catalog := mining.FileCatalog{Files: []mining.LaunchFile{
		{Path: `C:\Mining\A\one.bat`, Name: "one.bat", Folder: "A", Kind: "script"},
		{Path: `C:\Mining\A\two.cmd`, Name: "two.cmd", Folder: "A", Kind: "script"},
		{Path: `C:\Mining\A\miner.exe`, Name: "miner.exe", Folder: "A", Kind: "executable"},
		{Path: `C:\Mining\B\three.bat`, Name: "three.bat", Folder: "B", Kind: "script"},
	}}
	favorites := []string{`c:/mining/a/two.cmd`, `C:\Mining\Gone\old.bat`}
	view := BuildMiningBrowser(catalog, favorites, url.Values{"folder": {"A"}})
	if len(view.Folders) != 2 || view.Folders[0].Count != 2 || view.FavoriteCount != 2 || len(view.Scripts) != 4 {
		t.Fatalf("bad catalog: %+v", view)
	}
	visible := 0
	for _, s := range view.Scripts {
		if s.Visible {
			visible++
		}
		if s.Name == "two.cmd" && (!s.Favorite || s.ProcessName != "miner.exe" || s.EditorURL == "") {
			t.Fatalf("bad script: %+v", s)
		}
		if s.Missing && s.EditorURL != "" {
			t.Fatal("missing file editable")
		}
	}
	if visible != 2 {
		t.Fatalf("visible=%d", visible)
	}
	view = BuildMiningBrowser(catalog, favorites, url.Values{"favorites": {"1"}})
	visible = 0
	for _, s := range view.Scripts {
		if s.Visible {
			visible++
			if !s.Favorite {
				t.Fatal("nonfavorite visible")
			}
		}
	}
	if visible != 2 {
		t.Fatalf("favorites visible=%d", visible)
	}
	view = BuildMiningBrowser(catalog, nil, url.Values{"folder": {"missing"}})
	if view.SelectedFolder != "A" {
		t.Fatalf("fallback=%s", view.SelectedFolder)
	}
	catalog.Files = append(catalog.Files, mining.LaunchFile{Name: "other.exe", Folder: "A", Kind: "executable"})
	if miningScriptProcess(catalog, catalog.Files[0]) != "" {
		t.Fatal("ambiguous EXE selected")
	}
	if _, ok := catalogScript(catalog, `c:/mining/a/one.bat`); !ok {
		t.Fatal("canonical BAT not found")
	}
	if _, ok := catalogScript(catalog, `C:\Mining\A\miner.exe`); ok {
		t.Fatal("EXE accepted as BAT")
	}
}

func TestMiningFavoriteRequiresAdmin(t *testing.T) {
	app := &App{}
	request := httptest.NewRequest(http.MethodPost, "/admin/mining/favorite", nil)
	response := httptest.NewRecorder()
	app.handleAdminMiningFavorite(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestMiningRawEditorValidatesCatalogAndProcess(t *testing.T) {
	catalog := mining.FileCatalog{Files: []mining.LaunchFile{
		{Path: `C:\Mining\A\start.bat`, Name: "start.bat", Folder: "A", Kind: "script"},
		{Path: `C:\Mining\A\miner.exe`, Name: "miner.exe", Folder: "A", Kind: "executable"},
		{Path: `C:\Mining\B\other.exe`, Name: "other.exe", Folder: "B", Kind: "executable"},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/files" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "invalid", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(catalog)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	app := &App{mining: mining.NewClient(base, "fixture-token")}
	q := url.Values{"path": {`c:/mining/a/start.bat`}}
	request := httptest.NewRequest(http.MethodGet, "/admin/mining/script?"+q.Encode(), nil)
	miner, options, err := app.miningEditorTarget(context.Background(), request)
	if err != nil || miner.ID != 0 || miner.ProcessName != "miner.exe" || len(options) != 1 || !options[0].Selected {
		t.Fatalf("miner=%+v options=%+v err=%v", miner, options, err)
	}
	for _, values := range []url.Values{
		{"path": {`C:\Outside\start.bat`}},
		{"path": {`C:\Mining\A\miner.exe`}},
		{"path": {`C:\Mining\A\start.bat`}, "process": {"other.exe"}},
	} {
		request = httptest.NewRequest(http.MethodGet, "/admin/mining/script?"+values.Encode(), nil)
		if _, _, err = app.miningEditorTarget(context.Background(), request); err == nil {
			t.Fatalf("invalid target accepted: %v", values)
		}
	}
}
