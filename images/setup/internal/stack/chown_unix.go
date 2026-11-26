//go:build unix

package stack

import (
	"os"
	"syscall"
)

func chownLike(path string, st os.FileInfo) error {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return os.Lchown(path, int(s.Uid), int(s.Gid))
	}
	return nil
}
