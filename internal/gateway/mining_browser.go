package gateway

import (
	"net/url"
	"sort"
	"strings"

	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/store"
)

type miningFolderView struct {
	Name  string
	Count int
}
type miningScriptView struct {
	mining.LaunchFile
	Favorite, Missing, Visible bool
	ProcessName, EditorURL     string
}
type miningBrowserView struct {
	Root, Message, SelectedFolder string
	Folders                       []miningFolderView
	Scripts                       []miningScriptView
	FavoriteOnly                  bool
	FavoriteCount                 int
}

func BuildMiningBrowser(catalog mining.FileCatalog, favorites []string, query url.Values) miningBrowserView {
	view := miningBrowserView{Root: catalog.Root, Message: catalog.Message, SelectedFolder: query.Get("folder"), FavoriteOnly: query.Get("favorites") == "1"}
	favoriteMap := map[string]string{}
	for _, path := range favorites {
		favoriteMap[store.MiningScriptKey(path)] = path
	}
	folders := map[string]int{}
	for _, file := range catalog.Files {
		if file.Kind != "script" {
			continue
		}
		_, favorite := favoriteMap[store.MiningScriptKey(file.Path)]
		process := miningScriptProcess(catalog, file)
		q := url.Values{"path": {file.Path}}
		if process != "" {
			q.Set("process", process)
		}
		view.Scripts = append(view.Scripts, miningScriptView{LaunchFile: file, Favorite: favorite, ProcessName: process, EditorURL: "/admin/mining/script?" + q.Encode()})
		delete(favoriteMap, store.MiningScriptKey(file.Path))
		folders[file.Folder]++
	}
	for _, path := range favoriteMap {
		parts := strings.Split(strings.ReplaceAll(path, "/", "\\"), "\\")
		view.Scripts = append(view.Scripts, miningScriptView{LaunchFile: mining.LaunchFile{Path: path, Name: parts[len(parts)-1], Folder: "Файл недоступен", Kind: "script"}, Favorite: true, Missing: true})
	}
	for name, count := range folders {
		view.Folders = append(view.Folders, miningFolderView{Name: name, Count: count})
	}
	sort.Slice(view.Folders, func(i, j int) bool {
		return strings.ToLower(view.Folders[i].Name) < strings.ToLower(view.Folders[j].Name)
	})
	if folders[view.SelectedFolder] == 0 && len(view.Folders) > 0 {
		view.SelectedFolder = view.Folders[0].Name
	}
	sort.Slice(view.Scripts, func(i, j int) bool {
		a, b := view.Scripts[i], view.Scripts[j]
		if a.Favorite != b.Favorite {
			return a.Favorite
		}
		return strings.ToLower(a.Path) < strings.ToLower(b.Path)
	})
	for i := range view.Scripts {
		script := &view.Scripts[i]
		if script.Favorite {
			view.FavoriteCount++
		}
		script.Visible = (!view.FavoriteOnly && script.Folder == view.SelectedFolder) || (view.FavoriteOnly && script.Favorite)
	}
	return view
}

func miningScriptProcess(catalog mining.FileCatalog, script mining.LaunchFile) string {
	names := map[string]string{}
	for _, file := range catalog.Files {
		if file.Kind == "executable" && file.Folder == script.Folder && minerProcessPattern.MatchString(file.Name) {
			names[strings.ToLower(file.Name)] = file.Name
		}
	}
	if len(names) == 1 {
		for _, name := range names {
			return name
		}
	}
	return ""
}

func catalogScript(catalog mining.FileCatalog, path string) (mining.LaunchFile, bool) {
	for _, file := range catalog.Files {
		if file.Kind == "script" && sameMiningScript(path, file.Path) {
			return file, true
		}
	}
	return mining.LaunchFile{}, false
}
