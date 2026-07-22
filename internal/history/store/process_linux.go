//go:build linux

package store

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var errProcessNotFound = errors.New("process not found")

func processStartHint(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errProcessNotFound
		}
		return "", err
	}
	closeParen := strings.LastIndexByte(string(data), ')')
	if closeParen < 0 {
		return "", errors.New("malformed process stat")
	}
	fields := strings.Fields(string(data[closeParen+1:]))
	if len(fields) <= 19 {
		return "", errors.New("short process stat")
	}
	if fields[0] == "Z" || fields[0] == "X" || fields[0] == "x" {
		return "", errProcessNotFound
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", fmt.Errorf("parse process start time: %w", err)
	}
	return fields[19], nil
}
