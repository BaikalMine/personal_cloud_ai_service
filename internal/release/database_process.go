package release

import "regexp"

// This identity is private recovery metadata, not authority to signal a PID.
// A failed observation must never be treated as proof that the writer exited.
type databaseProcessIdentity struct {
	Platform     string `json:"platform"`
	PID          int    `json:"pid"`
	Started      uint64 `json:"started"`
	BootID       string `json:"boot_id,omitempty"`
	PIDNamespace string `json:"pid_namespace,omitempty"`
}

var databaseBootID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var databasePIDNamespace = regexp.MustCompile(`^[0-9]+:[1-9][0-9]*$`)

func (i databaseProcessIdentity) valid() bool {
	if i.PID <= 0 || int64(i.PID) > 2147483647 || i.Started == 0 {
		return false
	}
	switch i.Platform {
	case "windows":
		return i.BootID == "" && i.PIDNamespace == ""
	case "linux":
		return databaseBootID.MatchString(i.BootID) && databasePIDNamespace.MatchString(i.PIDNamespace)
	default:
		return false
	}
}
