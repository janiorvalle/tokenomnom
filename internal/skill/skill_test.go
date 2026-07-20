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
	document := Document("1.2.3")
	if got, owned := Version(document); !owned || got != "1.2.3" || !strings.HasSuffix(strings.TrimSpace(string(document)), "<!-- tokenomnom-skill v1.2.3 -->") {
		t.Fatalf("versioned document marker = %q, %v", got, owned)
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
