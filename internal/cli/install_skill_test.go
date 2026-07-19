package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/skill"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/tui"
)

func TestInstallSkillFreshIdempotentAndJSON(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	for _, directory := range []string{codexDir, claudeDir} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	output, err := executeReport([]string{"install-skill"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"Codex: installed vdev", "Claude: installed vdev"} {
		if !strings.Contains(output, fragment) {
			t.Errorf("fresh install missing %q:\n%s", fragment, output)
		}
	}
	for _, directory := range []string{codexDir, claudeDir} {
		contents, err := os.ReadFile(skill.Path(directory))
		if err != nil {
			t.Fatal(err)
		}
		if version, owned := skill.Version(contents); !owned || version != "dev" {
			t.Fatalf("installed marker = %q, %v", version, owned)
		}
	}

	output, err = executeReport([]string{"install-skill"}, codexDir, claudeDir)
	if err != nil || strings.Count(output, "up to date vdev") != 2 {
		t.Fatalf("idempotent install = %v\n%s", err, output)
	}

	output, err = executeReport([]string{"install-skill", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeEnvelope(t, output)
	assertEnvelope(t, envelope, "install-skill")
	var data jsonInstallSkillData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Providers) != 2 || data.Providers[0].Action != "up_to_date" || data.Providers[0].Version != "dev" {
		t.Fatalf("install JSON = %+v", data)
	}
}

func TestInstallSkillUpgradeForeignForceAndRemove(t *testing.T) {
	rootPath := t.TempDir()
	root := discover.Root{Provider: discover.ProviderCodex, Path: rootPath, Exists: true}
	first, err := applySkill(root, "1.0.0", false, false)
	if err != nil || first.Action != "installed" {
		t.Fatalf("first install = %+v, %v", first, err)
	}
	updated, err := applySkill(root, "2.0.0", false, false)
	if err != nil || updated.Action != "updated" || updated.Previous != "1.0.0" || updated.Version != "2.0.0" {
		t.Fatalf("upgrade = %+v, %v", updated, err)
	}

	path := skill.Path(rootPath)
	if err := os.WriteFile(path, []byte("user-authored skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused, err := applySkill(root, "2.0.0", false, false)
	if err != nil || refused.Action != "refused_foreign" {
		t.Fatalf("foreign refusal = %+v, %v", refused, err)
	}
	contents, _ := os.ReadFile(path)
	if string(contents) != "user-authored skill\n" {
		t.Fatal("foreign file changed without --force")
	}
	forced, err := applySkill(root, "2.0.0", true, false)
	if err != nil || forced.Action != "installed" {
		t.Fatalf("forced install = %+v, %v", forced, err)
	}
	removed, err := applySkill(root, "2.0.0", false, true)
	if err != nil || removed.Action != "removed" || removed.Version != "2.0.0" {
		t.Fatalf("remove = %+v, %v", removed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("skill still exists after remove: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("foreign again"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused, err = applySkill(root, "2.0.0", false, true)
	if err != nil || refused.Action != "refused_foreign" {
		t.Fatalf("foreign remove refusal = %+v, %v", refused, err)
	}
	removed, err = applySkill(root, "2.0.0", true, true)
	if err != nil || removed.Action != "removed" {
		t.Fatalf("forced remove = %+v, %v", removed, err)
	}
}

func TestInstallSkillSkipsMissingRoots(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	output, err := executeReport([]string{"install-skill"}, filepath.Join(root, "missing-codex"), filepath.Join(root, "missing-claude"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(output, "skipped: no root") != 2 {
		t.Fatalf("missing roots output:\n%s", output)
	}
	output, err = executeReport([]string{"doctor", "--format", "json"}, filepath.Join(root, "missing-codex"), filepath.Join(root, "missing-claude"))
	if err != nil {
		t.Fatal(err)
	}
	var data jsonDoctorData
	if err := json.Unmarshal(decodeEnvelope(t, output).Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Offer != nil {
		t.Fatalf("doctor offer = %q, want null", *data.Offer)
	}
}

func TestDoctorReportsSkillStatusPrettyAndJSON(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	if err := os.Mkdir(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := skill.Write(skill.Path(codexDir), skill.Document("1.2.3")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(skill.Path(claudeDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill.Path(claudeDir), []byte("foreign"), 0o644); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(func(tx *store.Tx) error { return tx.SetMeta(skill.OfferMetaKey, skill.OfferDeclined) }); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := executeReport([]string{"doctor"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"Skills", "Offer:   declined", "Codex:   installed v1.2.3", "Claude:  foreign file present"} {
		if !strings.Contains(output, fragment) {
			t.Errorf("doctor skill output missing %q:\n%s", fragment, output)
		}
	}
	output, err = executeReport([]string{"doctor", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeEnvelope(t, output)
	var data jsonDoctorData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Skills) != 2 || data.Offer == nil || *data.Offer != skill.OfferDeclined || data.Skills[0].Version == nil || *data.Skills[0].Version != "1.2.3" || data.Skills[1].Status != "foreign file present" {
		t.Fatalf("doctor JSON skills = %+v", data.Skills)
	}
}

func TestDeclinedManualInstallAcceptsAndRemoveKeepsOffer(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	for _, directory := range []string{codexDir, claudeDir} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(func(tx *store.Tx) error { return tx.SetMeta(skill.OfferMetaKey, skill.OfferDeclined) }); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := executeReport([]string{"install-skill"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}
	assertStoredSkillOffer(t, stateDir, skill.OfferAccepted)
	if _, err := executeReport([]string{"install-skill", "--remove"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}
	assertStoredSkillOffer(t, stateDir, skill.OfferAccepted)
}

func TestDashboardSkillOfferProbesRootsAndInstalledSkill(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	if err := os.Mkdir(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	offer := newDashboardSkillOffer(codexDir, claudeDir)
	check, err := offer.Check()
	if err != nil || check.Answered || !check.HasRoots || check.Installed {
		t.Fatalf("fresh offer check = %+v, %v", check, err)
	}
	results, err := offer.Install()
	if err != nil || len(results) != 2 || !strings.Contains(results[0], "installed vdev") {
		t.Fatalf("dashboard install results = %v, %v", results, err)
	}
	if _, err := os.Stat(skill.Path(codexDir)); err != nil {
		t.Fatalf("dashboard installer did not write skill: %v", err)
	}
	check, err = offer.Check()
	if err != nil || !check.Installed {
		t.Fatalf("installed offer check = %+v, %v", check, err)
	}
	if err := offer.Record(tui.SkillOfferPreinstalled); err != nil {
		t.Fatal(err)
	}
	assertStoredSkillOffer(t, stateDir, skill.OfferPreinstalled)
}

func TestDashboardSkillOfferNoRootsDoesNotBurnOffer(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	offer := newDashboardSkillOffer(filepath.Join(root, "missing-codex"), filepath.Join(root, "missing-claude"))
	check, err := offer.Check()
	if err != nil || check.HasRoots || check.Answered || check.Installed {
		t.Fatalf("no-root offer check = %+v, %v", check, err)
	}
	assertStoredSkillOffer(t, stateDir, "")
}

func assertStoredSkillOffer(t *testing.T, stateDir, want string) {
	t.Helper()
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	info, err := database.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.SkillOffer != want {
		t.Fatalf("stored skill offer = %q, want %q", info.SkillOffer, want)
	}
}
