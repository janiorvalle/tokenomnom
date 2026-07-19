// Package xdg resolves tokenomnom's persistent state and configuration directories.
package xdg

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
)

// Options supplies injectable process and OS settings for path resolution.
type Options struct {
	Getenv func(string) string
	Home   string
	GOOS   string
}

// StateDir returns the directory containing tokenomnom's durable usage store.
func StateDir(options Options) (string, error) {
	getenv := options.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if value := getenv("TOKENOMNOM_STATE_DIR"); value != "" {
		return absolute(value)
	}

	goos := options.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "windows" {
		base := getenv("LOCALAPPDATA")
		if base == "" {
			if options.Home == "" {
				return "", errors.New("home directory is required when LOCALAPPDATA is unset")
			}
			base = filepath.Join(options.Home, "AppData", "Local")
		}
		return absolute(filepath.Join(base, "tokenomnom"))
	}

	if value := getenv("XDG_STATE_HOME"); value != "" {
		return absolute(filepath.Join(value, "tokenomnom"))
	}
	if options.Home == "" {
		return "", errors.New("home directory is required when no state override is set")
	}
	return absolute(filepath.Join(options.Home, ".local", "state", "tokenomnom"))
}

// ConfigDir returns the directory containing tokenomnom's user configuration.
func ConfigDir(options Options) (string, error) {
	getenv := options.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if value := getenv("TOKENOMNOM_CONFIG_DIR"); value != "" {
		return absolute(value)
	}

	goos := options.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "windows" {
		base := getenv("APPDATA")
		if base == "" {
			if options.Home == "" {
				return "", errors.New("home directory is required when APPDATA is unset")
			}
			base = filepath.Join(options.Home, "AppData", "Roaming")
		}
		return absolute(filepath.Join(base, "tokenomnom"))
	}

	if value := getenv("XDG_CONFIG_HOME"); value != "" {
		return absolute(filepath.Join(value, "tokenomnom"))
	}
	if options.Home == "" {
		return "", errors.New("home directory is required when no config override is set")
	}
	return absolute(filepath.Join(options.Home, ".config", "tokenomnom"))
}

func absolute(path string) (string, error) {
	value, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make state directory absolute: %w", err)
	}
	return value, nil
}
