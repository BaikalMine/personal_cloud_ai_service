//go:build windows

package release

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const hostRecoveryProtection = "windows-user-dpapi-v1"

func protectHostRecovery(plaintext, entropy []byte) ([]byte, error) {
	return hostRecoveryDPAPI(plaintext, entropy, true)
}
func unprotectHostRecovery(payload, entropy []byte) ([]byte, error) {
	return hostRecoveryDPAPI(payload, entropy, false)
}

func hostRecoveryDPAPI(data, entropy []byte, protect bool) ([]byte, error) {
	return hostMaterialDPAPI(data, entropy, protect, maxHostRecoveryBytes)
}

func protectHostPlan(data, entropy []byte) ([]byte, error) {
	return hostMaterialDPAPI(data, entropy, true, maxHostPlanBytes)
}

func unprotectHostPlan(data, entropy []byte) ([]byte, error) {
	return hostMaterialDPAPI(data, entropy, false, maxHostPlanBytes)
}

func hostMaterialDPAPI(data, entropy []byte, protect bool, maximum int) ([]byte, error) {
	if len(data) == 0 || len(data) > maximum || len(entropy) == 0 || len(entropy) > maxHostRecoveryBytes {
		return nil, ErrHostRecoveryUnavailable
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	additional := windows.DataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var out windows.DataBlob
	var err error
	// Current-user scope only: do not enable CRYPTPROTECT_LOCAL_MACHINE. No
	// description, UI prompt or recovery secret is placed in an audit message.
	if protect {
		err = windows.CryptProtectData(&in, nil, &additional, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, &additional, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	runtime.KeepAlive(data)
	runtime.KeepAlive(entropy)
	if out.Data != nil {
		defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(out.Data))))
	}
	if err != nil || out.Data == nil || out.Size == 0 || int64(out.Size) > int64(maximum) {
		return nil, ErrHostRecoveryUnavailable
	}
	allocated := unsafe.Slice(out.Data, int(out.Size))
	result := append([]byte(nil), allocated...)
	clear(allocated)
	return result, nil
}
