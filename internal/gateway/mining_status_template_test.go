package gateway

import (
	"bytes"
	"strings"
	"testing"
)

func TestMiningPriorityMessageReflectsRunningState(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, running := range []bool{false, true} {
		var out bytes.Buffer
		data := map[string]any{
			"CurrentUser": &User{Role: "admin"},
			"CanAccessMining": true,
			"Mining": MiningOverview{Available: true, Running: running},
			"MiningStatus": "priority_busy",
		}
		if err := tpl.ExecuteTemplate(&out, "app", data); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out.String(), "Не удалось запустить майнинг: в момент запроса"); got == running {
			t.Fatalf("running=%v: priority warning present=%v", running, got)
		}
	}
}
