package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/schedule"
)

type cliScheduleRunner struct{ calls []string }

func (runner *cliScheduleRunner) Run(name string, args ...string) error {
	runner.calls = append(runner.calls, strings.Join(append([]string{name}, args...), " "))
	return nil
}

func TestScheduleCLIInstallStatusDriftAndUninstall(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, "config")
	stateDir := filepath.Join(home, "state")
	binary := filepath.Join(home, "bin", "tokenomnom")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[schedule]\ninterval = \"24h\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("TOKENOMNOM_CONFIG_DIR", configDir)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)

	previousPlatform, previousExecutable, previousUserHome, previousRunner := schedulePlatform, scheduleExecutable, scheduleUserHome, scheduleRunner
	runner := &cliScheduleRunner{}
	schedulePlatform = "linux"
	scheduleExecutable = func() (string, error) { return binary, nil }
	scheduleUserHome = func() (string, error) { return home, nil }
	scheduleRunner = runner
	t.Cleanup(func() {
		schedulePlatform, scheduleExecutable, scheduleUserHome, scheduleRunner = previousPlatform, previousExecutable, previousUserHome, previousRunner
	})

	install := executeScheduleCommand(t, "schedule", "install")
	if !strings.Contains(install, "Installed systemd user timer") || !strings.Contains(install, "Re-run schedule install") {
		t.Fatalf("install output:\n%s", install)
	}
	status := executeScheduleCommand(t, "schedule", "status")
	for _, fragment := range []string{"Installed:           yes", "Configured interval: 24h", "Binary exists:       yes", "Interval drift:      no"} {
		if !strings.Contains(status, fragment) {
			t.Errorf("status missing %q:\n%s", fragment, status)
		}
	}
	doctor := executeScheduleCommand(t, "doctor", "--format", "json")
	if !strings.Contains(doctor, `"schedule":{"installed":true`) {
		t.Fatalf("doctor JSON missing installed schedule:\n%s", doctor)
	}

	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[schedule]\ninterval = \"12h\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status = executeScheduleCommand(t, "schedule", "status", "--format", "json")
	if !strings.Contains(status, `"interval_drift":true`) || !strings.Contains(status, `"configured_interval":"12h"`) {
		t.Fatalf("JSON drift status:\n%s", status)
	}

	timerPath := filepath.Join(home, ".config", "systemd", "user", "tokenomnom.timer")
	if err := os.WriteFile(timerPath, []byte("broken timer"), 0o600); err != nil {
		t.Fatal(err)
	}
	uninstall := executeScheduleCommand(t, "schedule", "uninstall", "--format", "json")
	if !strings.Contains(uninstall, `"uninstalled":true`) || !strings.Contains(uninstall, `"installed":false`) || !strings.Contains(uninstall, `"definition_exists":false`) {
		t.Fatalf("uninstall output:\n%s", uninstall)
	}
	if _, err := os.Stat(timerPath); !os.IsNotExist(err) {
		t.Fatalf("timer remains after uninstall: %v", err)
	}
	if !strings.Contains(strings.Join(runner.calls, "\n"), "systemctl --user disable --now tokenomnom.timer") {
		t.Fatalf("scheduler calls = %#v", runner.calls)
	}
}

func executeScheduleCommand(t *testing.T, args ...string) string {
	t.Helper()
	var output bytes.Buffer
	command := NewRootCommand()
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	if err := command.Execute(); err != nil {
		t.Fatalf("execute %v: %v\n%s", args, err, output.String())
	}
	return output.String()
}

var _ schedule.Runner = (*cliScheduleRunner)(nil)
