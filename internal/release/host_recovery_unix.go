//go:build !windows

package release

// Unix protection is the verified owner-only directory/file permissions. This
// format is NOT encrypted at rest and must not be exported as an ordinary backup.
const hostRecoveryProtection = "unix-owner-only-v1"

func protectHostRecovery(plaintext, _ []byte) ([]byte, error) {
	return append([]byte(nil), plaintext...), nil
}
func unprotectHostRecovery(payload, _ []byte) ([]byte, error) {
	return append([]byte(nil), payload...), nil
}

func protectHostPlan(data, entropy []byte) ([]byte, error) {
	return protectHostRecovery(data, entropy)
}

func unprotectHostPlan(data, entropy []byte) ([]byte, error) {
	return unprotectHostRecovery(data, entropy)
}
