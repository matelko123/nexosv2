//go:build !unix

package docker

import "io/fs"

type fileID struct{ dev, ino uint64 }

// allocated falls back to the apparent size where block counts are unavailable.
func allocated(info fs.FileInfo, _ map[fileID]bool) int64 {
	if info.IsDir() {
		return 0
	}
	return info.Size()
}
