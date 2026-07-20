// Package skill embeds and safely installs the tokenomnom agent skill.
package skill

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	markerPrefix = "<!-- tokenomnom-skill v"
	markerSuffix = " -->"
	versionToken = "{{VERSION}}"

	// OfferMetaKey and its values record the one-time dashboard offer.
	OfferMetaKey      = "skill_offer"
	OfferAccepted     = "accepted"
	OfferDeclined     = "declined"
	OfferPreinstalled = "preinstalled"
)

//go:embed SKILL.md
var template []byte

// Embedded returns a copy of the embedded skill template.
func Embedded() []byte {
	return append([]byte(nil), template...)
}

// Document returns the embedded skill with its install-time version marker.
func Document(version string) []byte {
	if version == "" {
		version = "dev"
	}
	return []byte(strings.ReplaceAll(string(template), versionToken, version))
}

// Version returns the owned tokenomnom marker version in a document.
func Version(contents []byte) (string, bool) {
	text := strings.TrimSpace(string(contents))
	lineStart := strings.LastIndex(text, "\n") + 1
	line := text[lineStart:]
	if !strings.HasPrefix(line, markerPrefix) || !strings.HasSuffix(line, markerSuffix) {
		return "", false
	}
	version := strings.TrimSuffix(strings.TrimPrefix(line, markerPrefix), markerSuffix)
	return version, version != "" && !strings.ContainsAny(version, " \t\r\n<>")
}

// Path returns one provider root's tokenomnom skill target.
func Path(root string) string {
	return filepath.Join(root, "skills", "tokenomnom", "SKILL.md")
}

// Inspect reports whether a target is absent, owned, or foreign.
func Inspect(path string) (version string, owned, exists bool, err error) {
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	version, owned = Version(contents)
	return version, owned, true, nil
}

// Write atomically replaces a skill document after its directory exists.
func Write(path string, contents []byte) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create skill directory: %w", err)
	}
	temp, err := os.CreateTemp(directory, ".SKILL.md-*")
	if err != nil {
		return fmt.Errorf("create temporary skill: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(contents); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace skill %q: %w", path, err)
	}
	return nil
}

// Remove deletes a skill and its now-empty tokenomnom directory.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.Remove(directory); err != nil && !os.IsNotExist(err) {
		// A non-empty directory is intentionally retained.
		if !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
			return err
		}
	}
	return nil
}
