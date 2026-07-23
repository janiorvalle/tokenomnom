package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedSkillContentGuard(t *testing.T) {
	contents := string(Embedded())
	for _, command := range []string{"summary", "daily", "monthly", "models", "heatmap", "pricing", "doctor", "sync", "export", "install-skill", "schedule status", "history status", "history index", "history search", "history list", "history prompts", "history show", "history stats", "history sample"} {
		if !strings.Contains(contents, "tokenomnom "+command) {
			t.Errorf("embedded skill does not mention command %q", command)
		}
	}
	for _, fragment := range []string{"--format json", "nomnom", "API list-price equivalents", "not actual bills", "warnings"} {
		if !strings.Contains(contents, fragment) {
			t.Errorf("embedded skill missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"`--no-sync` is supported only by `summary`, `daily`, `monthly`, `models`,",
		"Do not pass `--no-sync` to `doctor`",
		"`sync`, `vault`, `schedule`, or `install-skill`",
		"history search <query> --limit 50 --format json",
		"data.page.next_cursor",
		"Usage sync freshness",
		"Vault archive freshness",
		"History-index freshness",
		"changed_sources_since_index",
		"active_*",
		"settled_*",
		"status_reasons",
		"missing sources alone are not stale",
		"fixed settle window is 10 minutes",
		"Do not re-index to chase active-session drift",
		"synced transcript files no longer",
		"indexed source heads whose file is",
		"Codex-complete but Claude-partial",
		"`project` is the cross-provider grouping",
		"`--repo` stays strictly proven",
		"project_source",
		"Do not traverse provider directories",
		"--role assistant",
		"Assistant coverage exists only after explicit",
		"--root-only",
		"--thread-kind subagent",
		"coverage.thread_kind.unknown",
		"unknown relationship",
		"--group-by month,project",
		"--project-source git",
		"--min-stratum-size",
		"cwd-derived projects can still be task folders",
		"missing-but-preserved sources alone",
		"unknown share is acceptably small",
		"inspect thread evidence and relationships",
		"default seed is stable",
		"unsupported schema or index failure",
		"state the searched",
		"remaining active and",
	} {
		if !strings.Contains(contents, fragment) {
			t.Errorf("embedded skill missing %q", fragment)
		}
	}
	if strings.Contains(contents, "--group-by month,cwd") {
		t.Error("embedded skill retains the fragmented month,cwd fallback")
	}
	document := Document("1.2.3")
	if got, owned := Version(document); !owned || got != "1.2.3" || !strings.HasSuffix(strings.TrimSpace(string(document)), "<!-- tokenomnom-skill v1.2.3 -->") {
		t.Fatalf("versioned document marker = %q, %v", got, owned)
	}
}

func TestUpdateAvailableHandlesReleaseAndDevelopmentVersions(t *testing.T) {
	for _, test := range []struct {
		installed string
		current   string
		want      bool
	}{
		{"1.2.3", "1.2.3", false},
		{"1.2.2", "1.2.3", true},
		{"dev", "1.2.3", true},
		{"1.2.3", "dev", false},
		{"dev", "dev", false},
	} {
		if got := UpdateAvailable(test.installed, test.current); got != test.want {
			t.Errorf("UpdateAvailable(%q, %q) = %t, want %t", test.installed, test.current, got, test.want)
		}
	}
}

func TestWriteIsAtomicAndInspectRecognizesOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills", "tokenomnom", "SKILL.md")
	if err := Write(path, Document("dev")); err != nil {
		t.Fatal(err)
	}
	version, owned, exists, err := Inspect(path)
	if err != nil || !exists || !owned || version != "dev" {
		t.Fatalf("inspect = %q, %v, %v, %v", version, owned, exists, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "SKILL.md" {
		t.Fatalf("temporary install artifact remained: %+v", entries)
	}
	if err := Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("empty skill directory was not removed: %v", err)
	}
}
