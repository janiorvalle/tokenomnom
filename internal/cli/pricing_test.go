package cli

import (
	"bytes"
	"encoding/json"
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

func TestPricingSetRatePersistsAndSurfacesUserProvenance(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("TOKENOMNOM_CONFIG_DIR", configDir)

	output, err := executeCLI("pricing", "set-rate", "gpt-5.6-terra", "--input", "1.25", "--output", "6", "--cache-read", "0.125", "--cache-write", "1.5")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "Set user rate for gpt-5.6-terra") || !strings.Contains(output, "Saved to") || !strings.Contains(output, "User rate figures are estimates") {
		t.Fatalf("set-rate receipt = %s", output)
	}
	userRatesPath := filepath.Join(configDir, userRatesFileName)
	stored, err := os.ReadFile(userRatesPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), `"input": 1.25`) || !strings.Contains(string(stored), `"cache_write": 1.5`) {
		t.Fatalf("stored user rates = %s", stored)
	}

	output, err = executeCLI("pricing", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data jsonPricingData `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatal(err)
	}
	var terra *jsonPricingEntry
	for _, model := range envelope.Data.Models {
		if model.Model == "gpt-5.6-terra" && len(model.Entries) == 1 {
			terra = &model.Entries[0]
		}
	}
	if terra == nil || terra.Status != "user" || terra.Provenance != "user" || terra.Source != "user rate" || terra.BaseInput == nil || *terra.BaseInput != 1.25 || terra.Output == nil || *terra.Output != 6 {
		t.Fatalf("user pricing JSON = %+v", terra)
	}

	output, err = executeCLI("pricing", "set-rate", "gpt-5.6-terra", "--clear", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var cleared struct {
		Data jsonPricingMutation `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.Data.Action != "clear" || !cleared.Data.Changed || cleared.Data.UserRate != nil {
		t.Fatalf("clear receipt = %+v", cleared.Data)
	}
	table, err := loadPricingTable()
	if err != nil {
		t.Fatal(err)
	}
	if _, found := table.RateFor("gpt-5.6-terra", "2026-08-02"); found {
		t.Fatal("cleared user rate remained effective")
	}
}

func TestPricingSetRateJSONErrorSuggestsKnownModel(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("TOKENOMNOM_CONFIG_DIR", configDir)

	output, err := executeCLI("pricing", "set-rate", "gpt-5.2-codxe", "--input", "1", "--output", "5", "--format", "json")
	if err == nil {
		t.Fatal("unknown model did not fail")
	}
	var envelope struct {
		Data struct {
			Error pricingCommandError `json:"error"`
		} `json:"data"`
	}
	if decodeErr := json.Unmarshal([]byte(output), &envelope); decodeErr != nil {
		t.Fatalf("JSON error output = %q: %v", output, decodeErr)
	}
	if envelope.Data.Error.Code != "UNKNOWN_MODEL" || !strings.Contains(envelope.Data.Error.Message, "gpt-5.2-codxe") || !strings.Contains(envelope.Data.Error.Example, "gpt-5.2-codex") {
		t.Fatalf("unknown model JSON error = %+v", envelope.Data.Error)
	}
}

func TestPricingSetRateJSONErrorsCoverArgumentsAndFlags(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("TOKENOMNOM_CONFIG_DIR", configDir)
	for _, test := range []struct {
		name string
		args []string
		code string
	}{
		{name: "missing model", args: []string{"pricing", "set-rate", "--format", "json"}, code: "MISSING_MODEL"},
		{name: "unknown flag before format", args: []string{"pricing", "set-rate", "gpt-5.6-terra", "--bogus", "--format", "json"}, code: "INVALID_FLAGS"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := executeCLI(test.args...)
			if err == nil {
				t.Fatal("invalid pricing command did not fail")
			}
			var envelope struct {
				Data struct {
					Error pricingCommandError `json:"error"`
				} `json:"data"`
			}
			if decodeErr := json.Unmarshal([]byte(output), &envelope); decodeErr != nil {
				t.Fatalf("JSON error output = %q (returned error %v): %v", output, err, decodeErr)
			}
			if envelope.Data.Error.Code != test.code {
				t.Fatalf("error code = %q, want %q", envelope.Data.Error.Code, test.code)
			}
		})
	}
	output, err := executeCLI("pricing", "set-rate", "gpt-5.6-terra", "--bogus")
	if err == nil || !strings.Contains(output, "code INVALID_FLAGS") || strings.Contains(output, `"schema":"tokenomnom.report/v1"`) {
		t.Fatalf("pretty flag error output = %q, error=%v", output, err)
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
