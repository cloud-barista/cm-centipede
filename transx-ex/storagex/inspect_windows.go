//go:build windows

package storagex

import (
	"os"
	"syscall"
)

// devFromInfo returns the volume serial number of path, the Windows analogue of
// st_dev, or false when it cannot be read.
func devFromInfo(path string, _ os.FileInfo) (uint64, bool) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	// FILE_FLAG_BACKUP_SEMANTICS is required to open a directory handle.
	h, err := syscall.CreateFile(p, 0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, false
	}
	defer syscall.CloseHandle(h)
	var fi syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &fi); err != nil {
		return 0, false
	}
	return uint64(fi.VolumeSerialNumber), true
}
