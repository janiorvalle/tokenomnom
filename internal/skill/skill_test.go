package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedSkillContentGuard(t *testing.T) {
	contents := string(Embedded())
	for _, command := range []string{"summary", "daily", "monthly", "models", "heatmap", "pricing", "doctor", "sync", "export", "install-skill", "vault list", "vault cat", "schedule status"} {
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
		"vault list --limit 100 --latest --format json",
		"data.page.next_cursor",
		"Usage sync freshness",
		"Vault archive freshness",
		"History-index freshness",
		"temporary fallback",
	} {
		if !strings.Contains(contents, fragment) {
			t.Errorf("embedded skill missing %q", fragment)
		}
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
