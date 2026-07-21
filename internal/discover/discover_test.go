package discover

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolvePrecedence(t *testing.T) {
	tempDir := t.TempDir()
	tests := []struct {
		name        string
		options     ResolveOptions
		environment map[string]string
		wantPaths   map[Provider]string
		wantSources map[Provider]string
	}{
		{
			name: "flags win",
			options: ResolveOptions{
				CodexDir:  filepath.Join(tempDir, "flag-codex"),
				ClaudeDir: filepath.Join(tempDir, "flag-claude"),
			},
			environment: map[string]string{
				"TOKENOMNOM_CODEX_DIR":  filepath.Join(tempDir, "env-codex"),
				"TOKENOMNOM_CLAUDE_DIR": filepath.Join(tempDir, "env-claude"),
				"CODEX_HOME":            filepath.Join(tempDir, "native-codex"),
				"CLAUDE_CONFIG_DIR":     filepath.Join(tempDir, "native-claude"),
			},
			wantPaths: map[Provider]string{
				ProviderCodex:  filepath.Join(tempDir, "flag-codex"),
				ProviderClaude: filepath.Join(tempDir, "flag-claude"),
			},
			wantSources: map[Provider]string{ProviderCodex: "flag", ProviderClaude: "flag"},
		},
		{
			name: "tokenomnom environment wins",
			environment: map[string]string{
				"TOKENOMNOM_CODEX_DIR":  filepath.Join(tempDir, "env-codex"),
				"TOKENOMNOM_CLAUDE_DIR": filepath.Join(tempDir, "env-claude"),
				"CODEX_HOME":            filepath.Join(tempDir, "native-codex"),
				"CLAUDE_CONFIG_DIR":     filepath.Join(tempDir, "native-claude"),
			},
			wantPaths: map[Provider]string{
				ProviderCodex:  filepath.Join(tempDir, "env-codex"),
				ProviderClaude: filepath.Join(tempDir, "env-claude"),
			},
			wantSources: map[Provider]string{
				ProviderCodex:  "env:TOKENOMNOM_CODEX_DIR",
				ProviderClaude: "env:TOKENOMNOM_CLAUDE_DIR",
			},
		},
		{
			name: "native environment wins",
			environment: map[string]string{
				"CODEX_HOME":        filepath.Join(tempDir, "native-codex"),
				"CLAUDE_CONFIG_DIR": filepath.Join(tempDir, "native-claude"),
			},
			wantPaths: map[Provider]string{
				ProviderCodex:  filepath.Join(tempDir, "native-codex"),
				ProviderClaude: filepath.Join(tempDir, "native-claude"),
			},
			wantSources: map[Provider]string{
				ProviderCodex:  "env:CODEX_HOME",
				ProviderClaude: "env:CLAUDE_CONFIG_DIR",
			},
		},
		{
			name: "defaults",
			wantPaths: map[Provider]string{
				ProviderCodex:  filepath.Join(tempDir, ".codex"),
				ProviderClaude: filepath.Join(tempDir, ".claude"),
			},
			wantSources: map[Provider]string{ProviderCodex: "default", ProviderClaude: "default"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := tt.options
			options.Home = tempDir
			options.Getenv = func(key string) string { return tt.environment[key] }

			roots, err := Resolve(options)
			if err != nil {
				t.Fatalf("resolve roots: %v", err)
			}
			if len(roots) != 2 {
				t.Fatalf("got %d roots, want 2", len(roots))
			}

			for _, root := range roots {
				if root.Path != tt.wantPaths[root.Provider] {
					t.Errorf("%s path = %q, want %q", root.Provider, root.Path, tt.wantPaths[root.Provider])
				}
				if root.Source != tt.wantSources[root.Provider] {
					t.Errorf("%s source = %q, want %q", root.Provider, root.Source, tt.wantSources[root.Provider])
				}
			}
		})
	}
}

func TestResolveReportsExistingRoots(t *testing.T) {
	tempDir := t.TempDir()
	codexDir := filepath.Join(tempDir, "codex")
	if err := os.Mkdir(codexDir, 0o755); err != nil {
		t.Fatalf("create codex root: %v", err)
	}

	roots, err := Resolve(ResolveOptions{
		CodexDir:  codexDir,
		ClaudeDir: filepath.Join(tempDir, "missing-claude"),
		Home:      tempDir,
	})
	if err != nil {
		t.Fatalf("resolve roots: %v", err)
	}
	if !roots[0].Exists {
		t.Error("existing Codex root reported missing")
	}
	if roots[1].Exists {
		t.Error("missing Claude root reported existing")
	}
}

func TestListSourceFiles(t *testing.T) {
	tempDir := t.TempDir()
	codexRoot := filepath.Join(tempDir, "codex")
	claudeRoot := filepath.Join(tempDir, "claude")

	files := map[string]string{
		filepath.Join(codexRoot, "sessions", "2026", "06", "13", "nested.jsonl"): "12345",
		filepath.Join(codexRoot, "archived_sessions", "archived.jsonl"):          "123",
		filepath.Join(codexRoot, "sessions", "ignore.txt"):                       "not jsonl",
		filepath.Join(claudeRoot, "projects", "project-a", "session.jsonl"):      "1234567",
		filepath.Join(claudeRoot, "other", "outside.jsonl"):                      "ignored",
	}
	for path, contents := range files {
		writeFixtureFile(t, path, contents)
	}

	tests := []struct {
		name      string
		root      Root
		wantSize  int64
		wantFiles int
	}{
		{"nested Codex sessions and archive", Root{Provider: ProviderCodex, Path: codexRoot}, 8, 2},
		{"Claude projects", Root{Provider: ProviderClaude, Path: claudeRoot}, 7, 1},
		{"empty root", Root{Provider: ProviderCodex, Path: filepath.Join(tempDir, "empty")}, 0, 0},
		{"missing root", Root{Provider: ProviderClaude, Path: filepath.Join(tempDir, "missing")}, 0, 0},
	}
	if err := os.Mkdir(filepath.Join(tempDir, "empty"), 0o755); err != nil {
		t.Fatalf("create empty root: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFiles, gotErrors := ListSourceFiles(tt.root)
			if len(gotErrors) != 0 {
				t.Fatalf("unexpected walk errors: %v", gotErrors)
			}
			if len(gotFiles) != tt.wantFiles {
				t.Fatalf("got %d files, want %d: %#v", len(gotFiles), tt.wantFiles, gotFiles)
			}
			var size int64
			for _, file := range gotFiles {
				size += file.Size
				if file.Provider != tt.root.Provider {
					t.Errorf("file provider = %q, want %q", file.Provider, tt.root.Provider)
				}
				if file.ModTime.IsZero() {
					t.Errorf("file %q has zero mod time", file.Path)
				}
			}
			if size != tt.wantSize {
				t.Errorf("total size = %d, want %d", size, tt.wantSize)
			}
		})
	}
}

func TestListSourceFilesClassifiesProviderSourceKinds(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, filepath.Join(root, "sessions", "live.jsonl"), "live\n")
	writeFixtureFile(t, filepath.Join(root, "archived_sessions", "archive.jsonl"), "archive\n")

	files, errs := ListSourceFiles(Root{Provider: ProviderCodex, Path: root})
	if len(errs) != 0 || len(files) != 2 {
		t.Fatalf("files=%#v errors=%v", files, errs)
	}
	got := map[string]SourceKind{}
	for _, file := range files {
		got[filepath.Base(file.Path)] = file.Kind
	}
	if got["live.jsonl"] != SourceCodexLive || got["archive.jsonl"] != SourceCodexArchive {
		t.Fatalf("source kinds = %#v", got)
	}
	claudeRoot := t.TempDir()
	writeFixtureFile(t, filepath.Join(claudeRoot, "projects", "project", "claude.jsonl"), "claude\n")
	claudeFiles, errs := ListSourceFiles(Root{Provider: ProviderClaude, Path: claudeRoot})
	if len(errs) != 0 || len(claudeFiles) != 1 || claudeFiles[0].Kind != SourceClaudeProject {
		t.Fatalf("Claude source kinds = %#v errors=%v", claudeFiles, errs)
	}
}

func TestListSourceFilesFollowsRootSymlink(t *testing.T) {
	tempDir := t.TempDir()
	realRoot := filepath.Join(tempDir, "real")
	writeFixtureFile(t, filepath.Join(realRoot, "sessions", "session.jsonl"), "data")

	linkRoot := filepath.Join(tempDir, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	files, walkErrors := ListSourceFiles(Root{Provider: ProviderCodex, Path: linkRoot})
	if len(walkErrors) != 0 {
		t.Fatalf("unexpected walk errors: %v", walkErrors)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
}

func TestListSourceFilesRecordsRootErrors(t *testing.T) {
	invalidPath := filepath.Join(t.TempDir(), string([]byte{0}))

	files, walkErrors := ListSourceFiles(Root{Provider: ProviderCodex, Path: invalidPath})
	if len(files) != 0 {
		t.Fatalf("got %d files from invalid root, want 0", len(files))
	}
	if len(walkErrors) != 1 {
		t.Fatalf("got %d walk errors, want 1: %v", len(walkErrors), walkErrors)
	}
}

func writeFixtureFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	modTime := time.Date(2026, time.June, 13, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set fixture mod time: %v", err)
	}
}
