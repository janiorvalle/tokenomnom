package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/version"
)

func TestRootCommandShowsHelpWithNoArguments(t *testing.T) {
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute root command: %v", err)
	}

	if !strings.Contains(output.String(), "Your agents nom tokens") {
		t.Fatalf("help output missing tagline:\n%s", output.String())
	}
}

func TestRootCommandShowsVersion(t *testing.T) {
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute --version: %v", err)
	}

	if !strings.Contains(output.String(), version.Version) {
		t.Fatalf("version output %q does not contain %q", output.String(), version.Version)
	}
}
