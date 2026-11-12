package release

import "testing"

func TestDatabaseProcessIdentityValidation(t *testing.T) {
	linux := databaseProcessIdentity{Platform: "linux", PID: 123, Started: 456,
		BootID: "12345678-1234-1234-1234-123456789abc", PIDNamespace: "5:4026531836"}
	windows := databaseProcessIdentity{Platform: "windows", PID: 123, Started: 456}
	if !linux.valid() || !windows.valid() {
		t.Fatal("valid identity refused")
	}
	for _, mutate := range []func(*databaseProcessIdentity){
		func(i *databaseProcessIdentity) { i.Platform = "" },
		func(i *databaseProcessIdentity) { i.PID = 0 },
		func(i *databaseProcessIdentity) { i.PID = -1 },
		func(i *databaseProcessIdentity) { i.Started = 0 },
		func(i *databaseProcessIdentity) { i.BootID = "" },
		func(i *databaseProcessIdentity) { i.BootID = "../boot" },
		func(i *databaseProcessIdentity) { i.PIDNamespace = "5:0" },
		func(i *databaseProcessIdentity) { i.PIDNamespace = "5:42\n" },
	} {
		identity := linux
		mutate(&identity)
		if identity.valid() {
			t.Fatalf("invalid identity accepted: %+v", identity)
		}
		if exited, err := databaseProcessExited(identity); err == nil || exited {
			t.Fatalf("invalid identity proves exit: %v, %v", exited, err)
		}
	}
	windows.BootID = linux.BootID
	if windows.valid() {
		t.Fatal("Windows identity accepted Linux fields")
	}
}
