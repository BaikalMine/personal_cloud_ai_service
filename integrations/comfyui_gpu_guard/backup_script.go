package comfyguardbackup

import _ "embed"

// Script restores only validated logical journal data into a new SQLite file.
// It is shared with the guard implementation and has no ComfyUI/CUDA imports.
//
//go:embed storage_backup.py
var Script string
