//go:build unix

package docker

import (
	"io/fs"
	"syscall"
)

type fileID struct{ dev, ino uint64 }

// allocated returns the disk space a file occupies (not its apparent size,
// which overstates sparse files). Hard-linked files are counted once.
func allocated(info fs.FileInfo, seen map[fileID]bool) int64 {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.Size()
	}
	if st.Nlink > 1 && !info.IsDir() {
		id := fileID{uint64(st.Dev), st.Ino}
		if seen[id] {
			return 0
		}
		seen[id] = true
	}
	return int64(st.Blocks) * 512
}
