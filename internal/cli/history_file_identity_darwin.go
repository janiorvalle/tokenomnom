//go:build darwin

package cli

import (
	"strings"
	"syscall"
)

func historyFilesystemHasReliableIdentity(path string) bool {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return false
	}
	var nameBuilder strings.Builder
	for _, value := range stat.Fstypename {
		if value == 0 {
			break
		}
		nameBuilder.WriteByte(byte(value))
	}
	name := nameBuilder.String()
	return name == "apfs"
}
