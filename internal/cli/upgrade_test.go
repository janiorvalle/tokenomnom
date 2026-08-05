package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/skill"
	"github.com/janiorvalle/tokenomnom/internal/upgrade"
	"github.com/janiorvalle/tokenomnom/internal/version"
)

type fakeUpgradeEngine struct {
	release    upgrade.Release
	available  bool
	result     upgrade.Result
	checkErr   error
	installErr error
	installs   int
}

func (engine *fakeUpgradeEngine) Check(context.Context) (upgrade.Release, bool, error) {
	return engine.release, engine.available, engine.checkErr
}

func (engine *fakeUpgradeEngine) Install(context.Context, upgrade.Release) (upgrade.Result, error) {
	engine.installs++
	return engine.result, engine.installErr
}

func TestUpgradeCheckExitCodesAndNeverTouchesSkill(t *testing.T) {
	setUpgradeTestVersion(t, "1.0.0")
	root := t.TempDir()
	codexDir := filepath.Join(root, "codex")
	if err := os.Mkdir(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := skill.Path(codexDir)
	if err := skill.Write(path, skill.Document("0.9.0")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	engine := &fakeUpgradeEngine{release: upgrade.Release{Version: "2.0.0", URL: "https://example.test/v2"}, available: true}
	runs := 0
	output, err := executeUpgradeCommand(t, engine, func(context.Context, string, ...string) ([]byte, []byte, error) { runs++; return nil, nil, nil }, codexDir, filepath.Join(root, "claude"), "--check")
	if !errors.Is(err, errUpgradeAvailable) || !strings.Contains(output, "Update available: v1.0.0 → v2.0.0") {
		t.Fatalf("available check = %q, %v", output, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) || engine.installs != 0 || runs != 0 {
		t.Fatalf("check mutated state: installs=%d runs=%d", engine.installs, runs)
	}

	engine.available = false
	output, err = executeUpgradeCommand(t, engine, func(context.Context, string, ...string) ([]byte, []byte, error) { runs++; return nil, nil, nil }, codexDir, filepath.Join(root, "claude"), "--check")
	if err != nil || !strings.Contains(output, "v1.0.0 is current") || runs != 0 {
		t.Fatalf("current check = %q, %v, runs=%d", output, err, runs)
	}
}

func TestUpgradeWithoutInstalledSkillDoesNotCreateOne(t *testing.T) {
	setUpgradeTestVersion(t, "1.0.0")
	root := t.TempDir()
	codexDir := filepath.Join(root, "codex")
	if err := os.Mkdir(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	engine := &fakeUpgradeEngine{
		release: upgrade.Release{Version: "2.0.0", URL: "https://example.test/v2"}, available: true,
		result: upgrade.Result{PreviousVersion: "1.0.0", Version: "2.0.0", ReleaseURL: "https://example.test/v2", ExecutablePath: "/tmp/new-tokenomnom"},
	}
	runs := 0
	output, err := executeUpgradeCommand(t, engine, func(context.Context, string, ...string) ([]byte, []byte, error) { runs++; return nil, nil, nil }, codexDir, filepath.Join(root, "claude"))
	if err != nil || engine.installs != 1 || runs != 0 || !strings.Contains(output, "Agent skill not installed") {
		t.Fatalf("upgrade = %q, err=%v installs=%d runs=%d", output, err, engine.installs, runs)
	}
	if _, err := os.Stat(skill.Path(codexDir)); !os.IsNotExist(err) {
		t.Fatalf("upgrade installed an unrequested skill: %v", err)
	}
}

func TestUpgradeRefreshesInstalledSkillWithNewBinary(t *testing.T) {
	setUpgradeTestVersion(t, "1.0.0")
	root := t.TempDir()
	codexDir := filepath.Join(root, "codex")
	if err := os.Mkdir(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := skill.Path(codexDir)
	if err := skill.Write(path, skill.Document("0.8.0")); err != nil {
		t.Fatal(err)
	}
	engine := &fakeUpgradeEngine{
		release: upgrade.Release{Version: "2.0.0", URL: "https://example.test/v2"}, available: true,
		result: upgrade.Result{PreviousVersion: "1.0.0", Version: "2.0.0", ReleaseURL: "https://example.test/v2", ExecutablePath: filepath.Join(root, "bin", "tokenomnom")},
	}
	var gotPath string
	var gotArgs []string
	run := func(_ context.Context, commandPath string, args ...string) ([]byte, []byte, error) {
		gotPath, gotArgs = commandPath, append([]string(nil), args...)
		return skillRefreshReceipt("codex", "updated", "2.0.0"), nil, skill.Write(path, skill.Document("2.0.0"))
	}
	output, err := executeUpgradeCommand(t, engine, run, codexDir, filepath.Join(root, "claude"))
	if err != nil || !strings.Contains(output, "Refreshed the installed") {
		t.Fatalf("upgrade = %q, %v", output, err)
	}
	if gotPath != engine.result.ExecutablePath || len(gotArgs) != 8 || !reflect.DeepEqual(gotArgs[:6], []string{"install-skill", "--format", "json", "--skip-offer-state", "--codex-dir", codexDir}) || gotArgs[6] != "--claude-dir" || gotArgs[7] == filepath.Join(root, "claude") {
		t.Fatalf("new binary invocation = %q %#v", gotPath, gotArgs)
	}
	contents, _ := os.ReadFile(path)
	if installedVersion, owned := skill.Version(contents); !owned || installedVersion != "2.0.0" {
		t.Fatalf("refreshed skill = %q, owned=%t", installedVersion, owned)
	}
	if _, err := os.Stat(skill.Path(filepath.Join(root, "claude"))); !os.IsNotExist(err) {
		t.Fatalf("refresh installed the absent Claude skill: %v", err)
	}
}

func TestCurrentBinaryRefreshesStaleInstalledSkill(t *testing.T) {
	setUpgradeTestVersion(t, "2.0.0")
	root := t.TempDir()
	codexDir := filepath.Join(root, "codex")
	if err := os.Mkdir(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := skill.Path(codexDir)
	if err := skill.Write(path, skill.Document("1.0.0")); err != nil {
		t.Fatal(err)
	}
	engine := &fakeUpgradeEngine{release: upgrade.Release{Version: "2.0.0", URL: "https://example.test/v2"}}
	runs := 0
	run := func(context.Context, string, ...string) ([]byte, []byte, error) {
		runs++
		return skillRefreshReceipt("codex", "updated", "2.0.0"), nil, skill.Write(path, skill.Document("2.0.0"))
	}
	output, err := executeUpgradeCommand(t, engine, run, codexDir, filepath.Join(root, "claude"))
	if err != nil || runs != 1 || engine.installs != 0 || !strings.Contains(output, "v2.0.0 is current") {
		t.Fatalf("current refresh = %q, err=%v runs=%d installs=%d", output, err, runs, engine.installs)
	}
}

func TestNewerPrereleaseRefreshUsesRunningVersion(t *testing.T) {
	setUpgradeTestVersion(t, "2.0.0-rc.1")
	root := t.TempDir()
	codexDir := filepath.Join(root, "codex")
	if err := os.Mkdir(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := skill.Write(skill.Path(codexDir), skill.Document("1.0.0")); err != nil {
		t.Fatal(err)
	}
	engine := &fakeUpgradeEngine{release: upgrade.Release{Version: "1.9.0", URL: "https://example.test/v1.9.0"}}
	output, err := executeUpgradeCommand(t, engine, func(context.Context, string, ...string) ([]byte, []byte, error) {
		return skillRefreshReceipt("codex", "updated", "2.0.0-rc.1"), nil, nil
	}, codexDir, filepath.Join(root, "claude"))
	if err != nil || !strings.Contains(output, "v2.0.0-rc.1 is current") {
		t.Fatalf("prerelease refresh = %q, %v", output, err)
	}
}

func TestUpgradeJSONSuppressesSkillSubprocessOutput(t *testing.T) {
	setUpgradeTestVersion(t, "2.0.0")
	root := t.TempDir()
	codexDir := filepath.Join(root, "codex")
	if err := os.Mkdir(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := skill.Write(skill.Path(codexDir), skill.Document("1.0.0")); err != nil {
		t.Fatal(err)
	}
	engine := &fakeUpgradeEngine{release: upgrade.Release{Version: "2.0.0", URL: "https://example.test/v2"}}
	output, err := executeUpgradeCommand(t, engine, func(context.Context, string, ...string) ([]byte, []byte, error) {
		return skillRefreshReceipt("codex", "updated", "2.0.0"), []byte("warning: tolerated unknown config key\n"), nil
	}, codexDir, filepath.Join(root, "claude"), "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeEnvelope(t, output)
	assertEnvelope(t, envelope, "upgrade")
	if strings.Contains(output, "Codex: updated") {
		t.Fatalf("child output corrupted JSON: %s", output)
	}
}

func TestUpgradeRejectsRefusedSkillRefreshReceipt(t *testing.T) {
	setUpgradeTestVersion(t, "2.0.0")
	root := t.TempDir()
	codexDir := filepath.Join(root, "codex")
	if err := os.Mkdir(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := skill.Write(skill.Path(codexDir), skill.Document("1.0.0")); err != nil {
		t.Fatal(err)
	}
	engine := &fakeUpgradeEngine{release: upgrade.Release{Version: "2.0.0", URL: "https://example.test/v2"}}
	_, err := executeUpgradeCommand(t, engine, func(context.Context, string, ...string) ([]byte, []byte, error) {
		return skillRefreshReceipt("codex", "refused_foreign", ""), nil, nil
	}, codexDir, filepath.Join(root, "claude"))
	var upgradeError *upgrade.Error
	if !errors.As(err, &upgradeError) || upgradeError.Code != "TOKENOMNOM_UPGRADE_SKILL_REFRESH_FAILED" {
		t.Fatalf("refused refresh error = %v", err)
	}
}

func executeUpgradeCommand(t *testing.T, engine upgradeEngine, run func(context.Context, string, ...string) ([]byte, []byte, error), codexDir, claudeDir string, args ...string) (string, error) {
	t.Helper()
	var output bytes.Buffer
	command := newUpgradeCommandWithOptions(&codexDir, &claudeDir, upgradeCommandOptions{newEngine: func() (upgradeEngine, error) { return engine, nil }, runCommand: run})
	command.SetOut(&output)
	command.SetErr(&output)
	command.Flags().String("format", "pretty", "test output format")
	command.SetArgs(args)
	err := command.Execute()
	return output.String(), err
}

func setUpgradeTestVersion(t *testing.T, value string) {
	t.Helper()
	previous := version.Version
	version.Version = value
	t.Cleanup(func() { version.Version = previous })
}

func skillRefreshReceipt(provider, action, targetVersion string) []byte {
	return []byte(fmt.Sprintf(`{"schema":"tokenomnom.report/v1","command":"install-skill","data":{"providers":[{"provider":%q,"action":%q,"version":%q}]}}`, provider, action, targetVersion))
}
