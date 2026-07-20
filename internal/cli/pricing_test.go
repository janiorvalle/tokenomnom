package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPricingCommandRendersEmbeddedAndOverrideTables(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("TOKENOMNOM_CONFIG_DIR", configDir)

	output, err := executeCLI("pricing")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"MODEL", "BASE INPUT", "WRITE 5M", "STATUS", "EFFECTIVE", "SOURCE", "OVERRIDE", "gpt-5.3-codex-spark", "proxy", "claude-sonnet-5", "$12.50", "$0.10", "through 2026-08-31", pricingDisclaimer} {
		if !strings.Contains(output, fragment) {
			t.Errorf("embedded pricing output missing %q:\n%s", fragment, output)
		}
	}

	override := `{"gpt-5.2":[{"base_input":9,"output":20,"status":"estimated","source":"https://example.com/rate"}]}`
	if err := os.WriteFile(filepath.Join(configDir, "pricing.json"), []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err = executeCLI("pricing")
	if err != nil {
		t.Fatal(err)
	}
	var overriddenLine string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "gpt-5.2 ") {
			overriddenLine = line
			break
		}
	}
	if overriddenLine == "" || !strings.Contains(overriddenLine, "$9") || !strings.Contains(overriddenLine, "estimated") || !strings.HasSuffix(overriddenLine, "yes") {
		t.Fatalf("override marker line = %q\n%s", overriddenLine, output)
	}
}

func TestPricingCommandRejectsMalformedOverride(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("TOKENOMNOM_CONFIG_DIR", configDir)
	if err := os.WriteFile(filepath.Join(configDir, "pricing.json"), []byte(`{"gpt-5.2":`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := executeCLI("pricing")
	if err == nil || !strings.Contains(err.Error(), "load pricing override") || !strings.Contains(err.Error(), "decode pricing JSON") {
		t.Fatalf("pricing malformed override error = %v", err)
	}
}

func executeCLI(args ...string) (string, error) {
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), err
}
