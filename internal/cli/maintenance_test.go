package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

func TestAutoVaultRunsAfterSyncAndHonorsInterval(t *testing.T) {
	paths := setupMaintenanceTest(t, true, filepath.Join(t.TempDir(), "vault"))
	source := writeMaintenanceSource(t, paths.codexDir, "one.jsonl")

	first := executeMaintenanceCommand(t, paths, "sync")
	if !strings.Contains(first, "Auto-vault\n") || !strings.Contains(first, "1 archived") {
		t.Fatalf("first sync missing auto-vault details:\n%s", first)
	}
	database, err := store.Open(filepath.Join(paths.stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	last, err := database.Meta(lastAutoVaultMeta)
	if err != nil || last == "" {
		t.Fatalf("last auto-vault = %q, %v", last, err)
	}
	manifest, err := database.VaultFiles()
	database.Close()
	if err != nil || len(manifest) != 1 || filepath.Base(manifest[0].SourcePath) != filepath.Base(source) {
		t.Fatalf("manifest = %#v, %v", manifest, err)
	}

	second := executeMaintenanceCommand(t, paths, "sync")
	if strings.Contains(second, "Auto-vault") {
		t.Fatalf("interval guard did not suppress auto-vault:\n%s", second)
	}
	database, err = store.Open(filepath.Join(paths.stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	lastAgain, err := database.Meta(lastAutoVaultMeta)
	database.Close()
	if err != nil || lastAgain != last {
		t.Fatalf("last auto-vault changed inside guard: %q -> %q (%v)", last, lastAgain, err)
	}
}

func TestAutoVaultDisabledAndFailureAreNonFatal(t *testing.T) {
	disabledVault := filepath.Join(t.TempDir(), "disabled-vault")
	disabled := setupMaintenanceTest(t, false, disabledVault)
	writeMaintenanceSource(t, disabled.codexDir, "disabled.jsonl")
	output := executeMaintenanceCommand(t, disabled, "sync")
	if strings.Contains(output, "Auto-vault") {
		t.Fatalf("disabled auto-vault produced output:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(disabledVault, "vault.json")); !os.IsNotExist(err) {
		t.Fatalf("disabled auto-vault initialized vault: %v", err)
	}

	brokenVault := filepath.Join(t.TempDir(), "vault-file")
	if err := os.WriteFile(brokenVault, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := setupMaintenanceTest(t, true, brokenVault)
	writeMaintenanceSource(t, broken.codexDir, "broken.jsonl")
	output = executeMaintenanceCommand(t, broken, "sync")
	if !strings.Contains(output, "Sync complete") || !strings.Contains(output, "WARNING: auto-vault transcripts") {
		t.Fatalf("auto-vault failure was not a non-fatal warning:\n%s", output)
	}
}

func TestReportAutoVaultIsOneLineAndJSONCarriesDetails(t *testing.T) {
	paths := setupMaintenanceTest(t, true, filepath.Join(t.TempDir(), "vault"))
	writeMaintenanceSource(t, paths.codexDir, "report.jsonl")
	output := executeMaintenanceCommand(t, paths, "summary")
	if strings.Count(output, "Auto-vault:") != 1 || strings.Contains(output, "Auto-vault\n") {
		t.Fatalf("report auto-vault output is not one status line:\n%s", output)
	}

	paths = setupMaintenanceTest(t, true, filepath.Join(t.TempDir(), "json-vault"))
	writeMaintenanceSource(t, paths.codexDir, "json.jsonl")
	output = executeMaintenanceCommand(t, paths, "summary", "--format", "json")
	var envelope struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode summary JSON: %v\n%s", err, output)
	}
	if !strings.Contains(strings.Join(envelope.Warnings, "\n"), "auto-vault archived 1") {
		t.Fatalf("JSON warnings missing auto-vault details: %#v", envelope.Warnings)
	}
}

func TestScheduledSyncIsQuietAndSkipsHeldStore(t *testing.T) {
	paths := setupMaintenanceTest(t, false, filepath.Join(t.TempDir(), "vault"))
	writeMaintenanceSource(t, paths.codexDir, "scheduled.jsonl")
	output := executeMaintenanceCommand(t, paths, "sync", "--scheduled")
	if strings.Count(output, "\n") != 1 || !strings.HasPrefix(output, "sync complete:") {
		t.Fatalf("scheduled output is not one line:\n%s", output)
	}

	databasePath := filepath.Join(paths.stateDir, store.DatabaseName)
	release, err := store.Lock(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	output = executeMaintenanceCommand(t, paths, "sync", "--scheduled")
	if output != "skipped: store in use\n" {
		t.Fatalf("lock-held scheduled output = %q", output)
	}

	brokenVault := filepath.Join(t.TempDir(), "vault-file")
	if err := os.WriteFile(brokenVault, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := setupMaintenanceTest(t, true, brokenVault)
	writeMaintenanceSource(t, broken.codexDir, "scheduled-warning.jsonl")
	warningOutput := executeMaintenanceCommand(t, broken, "sync", "--scheduled")
	if strings.Count(warningOutput, "\n") != 1 || !strings.Contains(warningOutput, "warnings: 1") {
		t.Fatalf("scheduled warning output is not one summarized line:\n%s", warningOutput)
	}
}

func TestScheduledHistoryIndexIsOptInDueAndReportSyncNeverRunsIt(t *testing.T) {
	paths := setupMaintenanceTest(t, false, filepath.Join(t.TempDir(), "vault"))
	configPath := filepath.Join(paths.configDir, "config.toml")
	file, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("[history]\nauto_index = true\nauto_interval = \"24h\"\nproviders = [\"codex\"]\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	writeTextFixture(t, filepath.Join(paths.codexDir, "sessions", "history.jsonl"), historyCodexFixture("scheduled-history", "scheduled history prompt"))
	historyPath := filepath.Join(paths.stateDir, historystore.DatabaseName)
	executeMaintenanceCommand(t, paths, "summary")
	if _, err := os.Stat(historyPath); !os.IsNotExist(err) {
		t.Fatalf("ordinary report created history index: %v", err)
	}

	output := executeMaintenanceCommand(t, paths, "sync", "--scheduled", "--format", "json")
	var envelope struct {
		Data jsonSyncData `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode scheduled sync: %v\n%s", err, output)
	}
	if envelope.Data.AutoHistory == nil || !envelope.Data.AutoHistory.Ran || envelope.Data.AutoHistory.ErrorCount != 0 {
		t.Fatalf("scheduled history result = %+v", envelope.Data.AutoHistory)
	}
	health, err := historystore.InspectHealth(historyPath)
	if err != nil || !health.Exists || health.LastAttemptUnix == 0 || health.LastCompleteSuccessUnix != 0 || health.Prompts != 1 {
		t.Fatalf("scheduled history health=%+v err=%v", health, err)
	}

	output = executeMaintenanceCommand(t, paths, "sync", "--scheduled", "--format", "json")
	envelope = struct {
		Data jsonSyncData `json:"data"`
	}{}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.AutoHistory != nil {
		t.Fatalf("history maintenance ignored interval: %+v", envelope.Data.AutoHistory)
	}
}

func TestScheduledHistoryFailureIsOneNonFatalWarningAfterVaultAndOutsideUsageLock(t *testing.T) {
	paths := setupMaintenanceTest(t, true, filepath.Join(t.TempDir(), "vault"))
	writeMaintenanceSource(t, paths.codexDir, "history-order.jsonl")
	original := dueHistoryIndex
	dueHistoryIndex = func(_ *cobra.Command, _ []discover.Root) (autoHistoryResult, error) {
		databasePath := filepath.Join(paths.stateDir, store.DatabaseName)
		release, err := store.Lock(databasePath)
		if err != nil {
			t.Fatalf("usage lock remained held during history maintenance: %v", err)
		}
		release()
		database, err := store.Open(databasePath)
		if err != nil {
			t.Fatal(err)
		}
		files, err := database.VaultFiles()
		database.Close()
		if err != nil || len(files) != 1 {
			t.Fatalf("history ran before due vault maintenance: files=%d err=%v", len(files), err)
		}
		historyPath := filepath.Join(paths.stateDir, historystore.DatabaseName)
		historyRelease, err := historystore.Lock(historyPath)
		if err != nil {
			t.Fatal(err)
		}
		historyDatabase, err := historystore.Open(historyPath)
		if err != nil {
			historyRelease()
			t.Fatal(err)
		}
		if err := historyDatabase.RecordRun(time.Now(), 1); err != nil {
			historyDatabase.Close()
			historyRelease()
			t.Fatal(err)
		}
		historyDatabase.Close()
		historyRelease()
		return autoHistoryResult{Ran: true, ErrorCount: 1}, errors.New("synthetic history failure")
	}
	t.Cleanup(func() { dueHistoryIndex = original })
	output := executeMaintenanceCommand(t, paths, "sync", "--scheduled")
	if !strings.Contains(output, "sync complete:") || !strings.Contains(output, "warnings: 1") || strings.Count(output, "WARNING: auto-index history:") != 1 {
		t.Fatalf("scheduled history failure output=%q", output)
	}
	doctorOutput := executeMaintenanceCommand(t, paths, "doctor", "--format", "json")
	envelope := decodeEnvelope(t, doctorOutput)
	var doctor struct {
		History jsonHistoryHealth `json:"history"`
	}
	if err := json.Unmarshal(envelope.Data, &doctor); err != nil || doctor.History.LastErrorSummary == nil || !strings.Contains(*doctor.History.LastErrorSummary, "1 error") || len(envelope.Warnings) != 1 {
		t.Fatalf("doctor history failure err=%v history=%+v warnings=%+v", err, doctor.History, envelope.Warnings)
	}
}

func TestFilteredHistoryFailureRemainsVisibleWithoutAdvancingFullSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), historystore.DatabaseName)
	database, err := historystore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	if err := database.RecordScopedRun(first, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordScopedRun(second, 2, false); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	health, err := historystore.InspectHealth(path)
	if err != nil || health.LastAttemptUnix != second.Unix() || health.LastCompleteSuccessUnix != first.Unix() || health.LastRunErrorCount != 2 || health.LastErrorSummary == "" {
		t.Fatalf("filtered failure health=%+v err=%v", health, err)
	}
}

type maintenancePaths struct {
	root      string
	stateDir  string
	configDir string
	dataDir   string
	codexDir  string
	claudeDir string
	vaultDir  string
}

func setupMaintenanceTest(t *testing.T, auto bool, vaultDir string) maintenancePaths {
	t.Helper()
	root := t.TempDir()
	paths := maintenancePaths{
		root: root, stateDir: filepath.Join(root, "state"), configDir: filepath.Join(root, "config"),
		dataDir: filepath.Join(root, "data"), codexDir: filepath.Join(root, "codex"), claudeDir: filepath.Join(root, "claude"), vaultDir: vaultDir,
	}
	t.Setenv("TOKENOMNOM_STATE_DIR", paths.stateDir)
	t.Setenv("TOKENOMNOM_CONFIG_DIR", paths.configDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", paths.dataDir)
	if err := os.MkdirAll(paths.configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := "[backup]\nenabled = false\n[vault]\ndir = " + strconvQuote(vaultDir) + "\nmin_age = \"0s\"\nauto = " + map[bool]string{true: "true", false: "false"}[auto] + "\nauto_interval = \"24h\"\n"
	if err := os.WriteFile(filepath.Join(paths.configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return paths
}

func writeMaintenanceSource(t *testing.T, codexDir, name string) string {
	t.Helper()
	path := filepath.Join(codexDir, "sessions", name)
	writeTextFixture(t, path,
		"{\"timestamp\":\"2026-07-18T09:00:00Z\",\"type\":\"turn_context\",\"payload\":{\"model\":\"gpt-test\"}}\n"+
			"{\"timestamp\":\"2026-07-18T10:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"last_token_usage\":{\"input_tokens\":5,\"output_tokens\":2}}}}\n")
	settled := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, settled, settled); err != nil {
		t.Fatal(err)
	}
	return path
}

func executeMaintenanceCommand(t *testing.T, paths maintenancePaths, args ...string) string {
	t.Helper()
	var output bytes.Buffer
	command := NewRootCommand()
	command.SetOut(&output)
	command.SetErr(&output)
	base := []string{"--codex-dir", paths.codexDir, "--claude-dir", paths.claudeDir}
	command.SetArgs(append(args, base...))
	if err := command.Execute(); err != nil {
		t.Fatalf("execute %v: %v\n%s", args, err, output.String())
	}
	return output.String()
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
