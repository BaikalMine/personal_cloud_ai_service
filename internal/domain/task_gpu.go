package domain

import "time"

// TaskGPUStatus is a point-in-time read, not a lease or permission to dispatch.
// Position counts eligible waiting intents across all kinds, excluding holders.
type TaskGPUStatus struct {
	Code       string    `json:"code"`
	Position   int       `json:"position,omitempty"`
	SnapshotAt time.Time `json:"snapshot_at"`
}

const TaskGPUSnapshotLimit = 32

type TaskGPUKey struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type TaskGPUSnapshotEntry struct {
	TaskGPUKey
	Available bool           `json:"available"`
	GPU       *TaskGPUStatus `json:"gpu"`
}

// Missing, foreign and permission-inaccessible sources share Available=false.
type TaskGPUSnapshots struct {
	SnapshotAt time.Time              `json:"snapshot_at"`
	Tasks      []TaskGPUSnapshotEntry `json:"tasks"`
}
