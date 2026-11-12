//go:build windows

package release

import (
	"reflect"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func databaseWriterTestWatch(t *testing.T, pid int) func() {
	t.Helper()
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(handle) })
	return func() {
		t.Helper()
		state, err := windows.WaitForSingleObject(handle, 5000)
		if err != nil || state != windows.WAIT_OBJECT_0 {
			t.Fatalf("writer still alive after parent termination: %v %d", err, state)
		}
	}
}

func TestDatabaseWriterWindowsEnvironment(t *testing.T) {
	got, err := databaseWriterWindowsEnvironment([]string{"z=last", "Alpha=first", "empty="})
	want := utf16.Encode([]rune("Alpha=first\x00empty=\x00z=last\x00\x00"))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("incorrect environment block: %v %v", got, err)
	}
	got, err = databaseWriterWindowsEnvironment([]string{})
	if err != nil || !reflect.DeepEqual(got, []uint16{0, 0}) {
		t.Fatalf("incorrect empty environment: %v %v", got, err)
	}
	for _, values := range [][]string{{"A=x", "a=y"}, {"bad"}, {"=C:=path"}, {"KEY=a\x00b"}} {
		if _, err := databaseWriterWindowsEnvironment(values); err == nil {
			t.Fatalf("accepted invalid environment: %#v", values)
		}
	}
}
