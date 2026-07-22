//go:build darwin

package store

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

var errProcessNotFound = errors.New("process not found")

func processStartHint(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.EIO) {
			return "", errProcessNotFound
		}
		return "", err
	}
	if info == nil || info.Proc.P_pid == 0 {
		return "", errProcessNotFound
	}
	if info.Proc.P_stat == 5 { // SZOMB
		return "", errProcessNotFound
	}
	started := info.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", started.Sec, started.Usec), nil
}
