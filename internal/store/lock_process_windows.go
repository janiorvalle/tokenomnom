//go:build windows

package store

import (
	"errors"

	"golang.org/x/sys/windows"
)

func isLockProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err == nil {
		_ = windows.CloseHandle(handle)
		return true
	}
	return errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
