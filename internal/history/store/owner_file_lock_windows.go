//go:build windows

package store

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var (
	historyKernel32         = syscall.NewLazyDLL("kernel32.dll")
	historyLockFileExProc   = historyKernel32.NewProc("LockFileEx")
	historyUnlockFileExProc = historyKernel32.NewProc("UnlockFileEx")
)

func tryLockHistoryOwnerFile(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := historyLockFileExProc.Call(
		file.Fd(),
		0x00000002|0x00000001, // LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY
		0, 1, 0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result == 0 {
		return callErr
	}
	return nil
}

func isHistoryOwnerFileLockBusy(err error) bool {
	return errors.Is(err, syscall.Errno(33)) // ERROR_LOCK_VIOLATION
}

func unlockHistoryOwnerFile(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := historyUnlockFileExProc.Call(
		file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)),
	)
	if result == 0 {
		return callErr
	}
	return nil
}
