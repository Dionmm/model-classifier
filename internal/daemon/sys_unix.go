//go:build darwin || linux

package daemon

import (
	"os"
	"syscall"
)

func sysUID(path string) int {
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(s.Uid)
	}
	return -1
}
