//go:build windows

package store

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

func isLockBusy(err error) bool {
	return errors.Is(err, syscall.Errno(33)) // ERROR_LOCK_VIOLATION
}

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	lockFileExProc   = kernel32.NewProc("LockFileEx")
	unlockFileExProc = kernel32.NewProc("UnlockFileEx")
)

func lockFile(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := lockFileExProc.Call(
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

func lockFileWait(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := lockFileExProc.Call(
		file.Fd(),
		0x00000002, // LOCKFILE_EXCLUSIVE_LOCK
		0, 1, 0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result == 0 {
		return callErr
	}
	return nil
}

func unlockFile(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := unlockFileExProc.Call(
		file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)),
	)
	if result == 0 {
		return callErr
	}
	return nil
}
