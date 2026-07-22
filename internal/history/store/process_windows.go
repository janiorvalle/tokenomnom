//go:build windows

package store

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

var errProcessNotFound = errors.New("process not found")

func processStartHint(pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return "", errProcessNotFound
		}
		return "", err
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return "", err
	}
	if exitCode != 259 { // STILL_ACTIVE
		return "", errProcessNotFound
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", creation.Nanoseconds()), nil
}
