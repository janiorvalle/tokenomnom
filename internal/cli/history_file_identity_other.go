//go:build !darwin && !windows

package cli

func historyFilesystemHasReliableIdentity(string) bool { return false }
