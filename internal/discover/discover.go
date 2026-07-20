// Package discover resolves provider data roots and enumerates their session files.
package discover

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Provider identifies a supported coding agent.
type Provider string

const (
	// ProviderCodex identifies Codex session data.
	ProviderCodex Provider = "codex"
	// ProviderClaude identifies Claude Code session data.
	ProviderClaude Provider = "claude"
)

// Root describes a provider data root and how it was selected.
type Root struct {
	Provider Provider
	Path     string
	Source   string
	Exists   bool
}

// SourceFile describes a discovered JSONL session file without reading it.
type SourceFile struct {
	Provider Provider
	Path     string
	Size     int64
	ModTime  time.Time
}

// ResolveOptions supplies explicit overrides and injectable system settings.
type ResolveOptions struct {
	CodexDir  string
	ClaudeDir string
	Home      string
	Getenv    func(string) string
}

// Resolve selects the Codex and Claude roots in flag, environment, native
// environment, then default order. Home and Getenv are injected so callers can
// resolve roots without the package consulting process-global state.
func Resolve(options ResolveOptions) ([]Root, error) {
	getenv := options.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	specs := []struct {
		provider      Provider
		flagValue     string
		tokenomnomEnv string
		nativeEnv     string
		defaultDir    string
	}{
		{ProviderCodex, options.CodexDir, "TOKENOMNOM_CODEX_DIR", "CODEX_HOME", ".codex"},
		{ProviderClaude, options.ClaudeDir, "TOKENOMNOM_CLAUDE_DIR", "CLAUDE_CONFIG_DIR", ".claude"},
	}

	roots := make([]Root, 0, len(specs))
	for _, spec := range specs {
		path, source, err := selectPath(spec.flagValue, spec.tokenomnomEnv, spec.nativeEnv, spec.defaultDir, options.Home, getenv)
		if err != nil {
			return nil, fmt.Errorf("resolve %s root: %w", spec.provider, err)
		}

		absolutePath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s root: make path absolute: %w", spec.provider, err)
		}

		_, statErr := os.Stat(absolutePath)
		exists := statErr == nil || !errors.Is(statErr, fs.ErrNotExist)
		roots = append(roots, Root{
			Provider: spec.provider,
			Path:     absolutePath,
			Source:   source,
			Exists:   exists,
		})
	}

	return roots, nil
}

func selectPath(flagValue, tokenomnomEnv, nativeEnv, defaultDir, home string, getenv func(string) string) (string, string, error) {
	if flagValue != "" {
		return flagValue, "flag", nil
	}
	if value := getenv(tokenomnomEnv); value != "" {
		return value, "env:" + tokenomnomEnv, nil
	}
	if value := getenv(nativeEnv); value != "" {
		return value, "env:" + nativeEnv, nil
	}
	if home == "" {
		return "", "", errors.New("home directory is required when no override is set")
	}
	return filepath.Join(home, defaultDir), "default", nil
}

// ListSourceFiles enumerates JSONL files in a provider's known session
// subtrees. Errors encountered while walking are returned separately so one
// unreadable path does not prevent other files from being discovered.
func ListSourceFiles(root Root) ([]SourceFile, []error) {
	resolvedRoot, err := filepath.EvalSymlinks(root.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("%s: %w", root.Path, err)}
	}

	subtrees := providerSubtrees(root.Provider)
	files := make([]SourceFile, 0)
	walkErrors := make([]error, 0)
	for _, subtree := range subtrees {
		start := filepath.Join(resolvedRoot, subtree)
		_, err := os.Lstat(start)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				walkErrors = append(walkErrors, fmt.Errorf("%s: %w", start, err))
			}
			continue
		}

		err = filepath.WalkDir(start, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				walkErrors = append(walkErrors, fmt.Errorf("%s: %w", path, walkErr))
				return nil
			}
			if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
				return nil
			}

			info, err := entry.Info()
			if err != nil {
				walkErrors = append(walkErrors, fmt.Errorf("%s: %w", path, err))
				return nil
			}
			if !info.Mode().IsRegular() {
				return nil
			}

			files = append(files, SourceFile{
				Provider: root.Provider,
				Path:     path,
				Size:     info.Size(),
				ModTime:  info.ModTime(),
			})
			return nil
		})
		if err != nil {
			walkErrors = append(walkErrors, fmt.Errorf("%s: %w", start, err))
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, walkErrors
}

func providerSubtrees(provider Provider) []string {
	switch provider {
	case ProviderCodex:
		return []string{"sessions", "archived_sessions"}
	case ProviderClaude:
		return []string{"projects"}
	default:
		return nil
	}
}
