//go:build !windows

package storagex

import (
	"os"
	"syscall"
)

// devFromInfo returns the device id (st_dev) from a FileInfo, or false when the
// underlying platform data is unavailable.
func devFromInfo(_ string, info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}
