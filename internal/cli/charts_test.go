package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

func TestPeriodChartsRenderStructureAndLastNotice(t *testing.T) {
	periods := make([]chartPeriod, 0, 30)
	for day := 1; day <= 30; day++ {
		label := "2026-06-" + twoDigits(day)
		periods = append(periods, chartPeriod{label: label, values: map[discover.Provider]providerChartValue{
			discover.ProviderCodex:  {Cost: aggregateCost{Total: pricing.Money(day) * 10_000_000}},
			discover.ProviderClaude: {Cost: aggregateCost{Total: pricing.Money(day) * 5_000_000}},
		}})
	}
	output := renderPeriodChart(styledRenderContext(50), periods, "day", "days", false)
	for _, fragment := range []string{"Codex", "Claude", "cost/day", "showing last", "of 30 days", "$", "█", "30"} {
		if !strings.Contains(output, fragment) {
			t.Errorf("daily chart missing %q:\n%s", fragment, output)
		}
	}
	assertRenderedWidth(t, output, 50)
	wideOutput := renderPeriodChart(styledRenderContext(200), periods, "day", "days", false)
	assertRenderedWidth(t, wideOutput, 200)
	if strings.Contains(wideOutput, "showing last") {
		t.Fatalf("200-column chart unexpectedly truncated 30 periods:\n%s", wideOutput)
	}

	months := []chartPeriod{
		{label: "2026-01", values: map[discover.Provider]providerChartValue{discover.ProviderCodex: {Cost: aggregateCost{Total: 1_000_000_000}}}},
		{label: "2026-02", values: map[discover.Provider]providerChartValue{discover.ProviderClaude: {Cost: aggregateCost{Total: 2_000_000_000}}}},
	}
	output = renderPeriodChart(styledRenderContext(80), months, "month", "months", false)
	for _, fragment := range []string{"cost/month", "01", "02", "2026", "$2.00"} {
		if !strings.Contains(output, fragment) {
			t.Errorf("monthly chart missing %q:\n%s", fragment, output)
		}
	}
	if strings.Contains(output, "showing last") {
		t.Fatalf("short monthly chart was unexpectedly truncated:\n%s", output)
	}
}

func TestPeriodChartFallsBackToTokens(t *testing.T) {
	periods := []chartPeriod{{label: "2026-07-18", values: map[discover.Provider]providerChartValue{
		discover.ProviderCodex:  {Tokens: 1_200},
		discover.ProviderClaude: {Tokens: 300},
	}}}
	output := renderPeriodChart(styledRenderContext(80), periods, "day", "days", true)
	for _, fragment := range []string{"tokens/day (unpriced)", "1,500", "█"} {
		if !strings.Contains(output, fragment) {
			t.Errorf("token fallback chart missing %q:\n%s", fragment, output)
		}
	}
}

func TestChartFallbackUsesPricingCoverageNotDollarTotal(t *testing.T) {
	if chartUsesTokens(reportCosts{Grand: aggregateCost{PricedTokens: 1}}) {
		t.Fatal("explicit zero-rate priced usage was mislabeled as unpriced")
	}
	if !chartUsesTokens(reportCosts{Grand: aggregateCost{UnpricedTokens: 1}}) {
		t.Fatal("entirely unpriced usage did not fall back to tokens")
	}
}

func TestPeriodChartDoesNotWrapBelowMinimumWidth(t *testing.T) {
	output := renderPeriodChart(styledRenderContext(31), []chartPeriod{{label: "2026-07-18"}}, "day", "days", false)
	if output != "" {
		t.Fatalf("narrow chart output = %q, want suppressed chart", output)
	}
	tokenOutput := renderPeriodChart(styledRenderContext(40), []chartPeriod{{label: "2026-07-18"}}, "month", "months", true)
	if tokenOutput != "" {
		t.Fatalf("token legend wider than terminal was not suppressed: %q", tokenOutput)
	}
}

func TestPeriodChartAddsSpanCaption(t *testing.T) {
	daily := []chartPeriod{{label: "2025-12-31"}, {label: "2026-01-01"}}
	output := renderPeriodChart(styledRenderContext(80), daily, "day", "days", false)
	for _, fragment := range []string{"31", "01", "Dec 2025 – Jan 2026"} {
		if !strings.Contains(output, fragment) {
			t.Errorf("cross-month daily chart missing %q:\n%s", fragment, output)
		}
	}

	sameMonth := []chartPeriod{{label: "2026-07-01"}, {label: "2026-07-02"}}
	output = renderPeriodChart(styledRenderContext(80), sameMonth, "day", "days", false)
	if !strings.Contains(output, "Jul 2026") || strings.Contains(output, "–") {
		t.Errorf("same-month daily caption wrong:\n%s", output)
	}

	monthly := []chartPeriod{{label: "2025-12"}, {label: "2026-01"}}
	output = renderPeriodChart(styledRenderContext(80), monthly, "month", "months", false)
	if !strings.Contains(output, "2025 – 2026") {
		t.Errorf("cross-year monthly chart missing span caption:\n%s", output)
	}
}

func TestNoChartAndPlainModesSuppressCharts(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)

	styled, err := executeStyledReport([]string{"daily", "--no-sync"}, codexDir, claudeDir, 80)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(styled, "■") || !strings.Contains(styled, "\x1b[") {
		t.Fatalf("styled daily output has no chart/color:\n%s", styled)
	}

	withoutChart, err := executeStyledReport([]string{"daily", "--no-sync", "--no-chart"}, codexDir, claudeDir, 80)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withoutChart, "■") || !strings.Contains(withoutChart, "\x1b[") {
		t.Fatalf("--no-chart removed styling or retained chart:\n%s", withoutChart)
	}

	plain, err := executeReport([]string{"daily", "--no-sync"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	noColor, err := executeStyledReport([]string{"daily", "--no-sync", "--no-color"}, codexDir, claudeDir, 80)
	if err != nil {
		t.Fatal(err)
	}
	if noColor != plain || strings.Contains(noColor, "\x1b[") || strings.Contains(noColor, "■") {
		t.Fatalf("--no-color output changed Plain bytes:\nplain:\n%s\nno-color:\n%s", plain, noColor)
	}
}

func styledRenderContext(width int) theme.Context {
	terminal := true
	dark := true
	return theme.Resolve(theme.ResolveOptions{
		Output: &bytes.Buffer{}, ForceTerminal: &terminal, Width: width,
		ForceColor: true, Dark: &dark, LookupEnv: func(string) (string, bool) { return "", false },
	})
}

func executeStyledReport(args []string, codexDir, claudeDir string, width int) (string, error) {
	var output bytes.Buffer
	terminal := true
	dark := true
	cmd := newRootCommand(theme.ResolveOptions{
		ForceTerminal: &terminal, Width: width, ForceColor: true, Dark: &dark,
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	args = append(args, "--codex-dir", codexDir, "--claude-dir", claudeDir)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), err
}

func assertRenderedWidth(t *testing.T, output string, width int) {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if visible := ansiVisibleWidth(line); visible > width {
			t.Errorf("chart line width = %d, want <= %d: %q", visible, width, line)
		}
	}
}

func ansiVisibleWidth(value string) int {
	inEscape := false
	width := 0
	for _, char := range value {
		if char == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if char == 'm' {
				inEscape = false
			}
			continue
		}
		width++
	}
	return width
}

func twoDigits(value int) string {
	return string([]byte{'0' + byte(value/10), '0' + byte(value%10)})
}
