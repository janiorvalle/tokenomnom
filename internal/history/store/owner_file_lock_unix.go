//go:build darwin || linux

package store

import (
	"errors"
	"os"
	"syscall"
)

func tryLockHistoryOwnerFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func isHistoryOwnerFileLockBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

func unlockHistoryOwnerFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
