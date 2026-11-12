package store_test

import (
	"ai-access-gateway/internal/store"
	"context"
	"testing"
)

func TestMiningFavoritesIntegration(t *testing.T) {
	db, repository, admin := comfyStorageDBFixture(t)
	ctx := context.Background()
	for _, path := range []string{`C:\Mining\Miner\start.bat`, `c:/mining/miner/start.bat`} {
		if err := repository.SetMiningScriptFavorite(ctx, admin, path, true); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := repository.MiningScriptFavorites(ctx, admin)
	if err != nil || len(paths) != 1 {
		t.Fatalf("favorites=%v err=%v", paths, err)
	}
	if store.MiningScriptKey(paths[0]) != `c:\mining\miner\start.bat` {
		t.Fatal(paths)
	}
	var other, user int64
	if err = db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('other-admin','hash','admin') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('regular-user','hash','user') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err = repository.SetMiningScriptFavorite(ctx, user, `C:\Mining\user.bat`, true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{other, user} {
		paths, err = repository.MiningScriptFavorites(ctx, id)
		if err != nil || len(paths) != 0 {
			t.Fatalf("role isolation: %v %v", paths, err)
		}
	}
	if err = repository.SetMiningScriptFavorite(ctx, admin, `C:/Mining/Miner/start.bat`, false); err != nil {
		t.Fatal(err)
	}
	paths, err = repository.MiningScriptFavorites(ctx, admin)
	if err != nil || len(paths) != 0 {
		t.Fatal(paths, err)
	}
	if err = repository.SetMiningScriptFavorite(ctx, admin, `C:\Mining\x.bat`, true); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM users WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM mining_script_favorites`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
