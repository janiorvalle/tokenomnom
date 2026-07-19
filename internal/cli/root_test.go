package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

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

func TestTimezoneFingerprintChangesWithRules(t *testing.T) {
	t.Parallel()
	first := timezoneFingerprint(time.FixedZone("Local", 0))
	second := timezoneFingerprint(time.FixedZone("Local", -5*60*60))
	if first == second || first == "" || second == "" {
		t.Fatalf("timezone fingerprints should differ: %q %q", first, second)
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

func TestLocalTimezoneNameUsesTZEnvironment(t *testing.T) {
	t.Setenv("TZ", "America/New_York")
	if got := localTimezoneName(); got != "America/New_York" {
		t.Fatalf("local timezone = %q", got)
	}
}
