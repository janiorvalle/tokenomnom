package xdg

import (
	"path/filepath"
	"testing"
)

func TestStateDir(t *testing.T) {
	t.Parallel()
	home := filepath.Join(t.TempDir(), "home")
	tests := []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{"explicit override", "linux", map[string]string{"TOKENOMNOM_STATE_DIR": filepath.Join(home, "state")}, filepath.Join(home, "state")},
		{"xdg state", "linux", map[string]string{"XDG_STATE_HOME": filepath.Join(home, "xdg")}, filepath.Join(home, "xdg", "tokenomnom")},
		{"unix default", "darwin", nil, filepath.Join(home, ".local", "state", "tokenomnom")},
		{"windows local app data", "windows", map[string]string{"LOCALAPPDATA": filepath.Join(home, "Local")}, filepath.Join(home, "Local", "tokenomnom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := StateDir(Options{Home: home, GOOS: tt.goos, Getenv: func(key string) string { return tt.env[key] }})
			if err != nil {
				t.Fatalf("StateDir() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("StateDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfigDir(t *testing.T) {
	t.Parallel()
	home := filepath.Join(t.TempDir(), "home")
	tests := []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{"explicit override", "linux", map[string]string{"TOKENOMNOM_CONFIG_DIR": filepath.Join(home, "config")}, filepath.Join(home, "config")},
		{"xdg config", "linux", map[string]string{"XDG_CONFIG_HOME": filepath.Join(home, "xdg")}, filepath.Join(home, "xdg", "tokenomnom")},
		{"unix default", "darwin", nil, filepath.Join(home, ".config", "tokenomnom")},
		{"windows app data", "windows", map[string]string{"APPDATA": filepath.Join(home, "Roaming")}, filepath.Join(home, "Roaming", "tokenomnom")},
		{"windows home fallback", "windows", nil, filepath.Join(home, "AppData", "Roaming", "tokenomnom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConfigDir(Options{Home: home, GOOS: tt.goos, Getenv: func(key string) string { return tt.env[key] }})
			if err != nil {
				t.Fatalf("ConfigDir() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ConfigDir() = %q, want %q", got, tt.want)
			}
		})
	}
}
