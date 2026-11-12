//go:build !windows

package updateagent

import "io/fs"

func comfyInventoryLink(info fs.FileInfo) bool { return info.Mode()&fs.ModeSymlink != 0 }
