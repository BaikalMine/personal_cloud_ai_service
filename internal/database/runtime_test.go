package database

import (
	"bytes"
	"encoding/json"
	"testing"

	"ai-access-gateway/internal/buildinfo"
)

func TestRuntimeFenceRecordValidation(t *testing.T) {
	initial := runtimeFenceRecord{Singleton: 1, RuntimeFenceState: RuntimeFenceState{ProtocolVersion: 1}}
	data, _ := json.Marshal(initial)
	if _, err := parseRuntimeFence(data); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, []byte(`{}`), bytes.Repeat([]byte("x"), 4097), bytes.Replace(data, []byte(`"active":false`), []byte(`"active":null`), 1), bytes.Replace(data, []byte(`"epoch":0`), []byte(`"epoch":-1`), 1), bytes.Replace(data, []byte(`"protocol_version":1`), []byte(`"protocol_version":2`), 1), bytes.Replace(data, []byte(`"active":false`), []byte(`"active":true`), 1)} {
		if _, err := parseRuntimeFence(bad); err == nil {
			t.Fatalf("bad fence accepted: %s", bad)
		}
	}
	locks := map[int64]bool{}
	for _, id := range []int64{runtimeControllerLockID, runtimeConnectionLockID, runtimeValidationLockID, migrationLockID} {
		if locks[id] {
			t.Fatal("advisory key collision")
		}
		locks[id] = true
	}
	for _, component := range []string{buildinfo.Gateway, buildinfo.InferenceWorker} {
		found := false
		for _, c := range buildinfo.Contracts(component) {
			if c.Name == "release-runtime-fence" {
				found = c.Version == RuntimeFenceProtocol
			}
		}
		if !found {
			t.Fatalf("%s does not advertise the runtime protocol", component)
		}
	}
}
