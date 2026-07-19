package theme

import (
	"bytes"
	"testing"
)

func TestResolveModeMatrix(t *testing.T) {
	terminal := true
	notTerminal := false
	unset := func(string) (string, bool) { return "", false }
	set := func(string) (string, bool) { return "", true }

	tests := []struct {
		name      string
		options   ResolveOptions
		want      Mode
		wantWidth int
	}{
		{name: "styled tty", options: ResolveOptions{ForceTerminal: &terminal, LookupEnv: unset, Width: 120, ForceColor: true}, want: Styled, wantWidth: 120},
		{name: "no color flag", options: ResolveOptions{NoColor: true, ForceTerminal: &terminal, LookupEnv: unset}, want: Plain, wantWidth: defaultWidth},
		{name: "no color environment even empty", options: ResolveOptions{ForceTerminal: &terminal, LookupEnv: set}, want: Plain, wantWidth: defaultWidth},
		{name: "non tty", options: ResolveOptions{ForceTerminal: &notTerminal, LookupEnv: unset}, want: Plain, wantWidth: defaultWidth},
		{name: "json", options: ResolveOptions{Format: "json", ForceTerminal: &terminal, LookupEnv: unset}, want: Plain, wantWidth: defaultWidth},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.options.Output = &bytes.Buffer{}
			got := Resolve(test.options)
			if got.Mode != test.want || got.Width != test.wantWidth {
				t.Fatalf("Resolve() = mode %v width %d, want mode %v width %d", got.Mode, got.Width, test.want, test.wantWidth)
			}
		})
	}
}

func TestProviderShadeAssignmentIsDeterministic(t *testing.T) {
	render := Resolve(ResolveOptions{Output: &bytes.Buffer{}})
	palette := render.Palette
	first := palette.ProviderColor("codex", 2)
	if got := palette.ProviderColor("codex", 2); got != first {
		t.Fatalf("same provider/rank changed color: %#v then %#v", first, got)
	}
	if got := palette.ProviderColor("codex", 7); got != first {
		t.Fatalf("ramp wrap = %#v, want %#v", got, first)
	}
	if got := palette.ProviderColor("claude", 2); got == first {
		t.Fatalf("provider ramps share rank-2 color: %#v", got)
	}
	if palette.HeatmapColor(-1) != palette.HeatmapColor(0) || palette.HeatmapColor(99) != palette.HeatmapColor(4) {
		t.Fatal("heatmap ramp did not clamp to its five defined steps")
	}
}

func TestAdaptivePaletteChangesForLightAndDarkBackgrounds(t *testing.T) {
	terminal := true
	dark, light := true, false
	options := ResolveOptions{
		Output: &bytes.Buffer{}, ForceTerminal: &terminal, ForceColor: true,
		LookupEnv: func(string) (string, bool) { return "", false },
	}
	options.Dark = &dark
	darkValue := Resolve(options).Palette.Provider("codex", 0).Render("Codex")
	options.Dark = &light
	lightValue := Resolve(options).Palette.Provider("codex", 0).Render("Codex")
	if darkValue == lightValue || darkValue == "Codex" || lightValue == "Codex" {
		t.Fatalf("adaptive provider style did not change: dark %q light %q", darkValue, lightValue)
	}
}

func TestBackgroundHeuristicsAreNonblockingAndAdaptive(t *testing.T) {
	terminal := true
	options := ResolveOptions{Output: &bytes.Buffer{}, ForceTerminal: &terminal, ForceColor: true}
	options.LookupEnv = func(key string) (string, bool) {
		if key == "COLORFGBG" {
			return "0;15", true
		}
		return "", false
	}
	if Resolve(options).Renderer.HasDarkBackground() {
		t.Fatal("light COLORFGBG was detected as dark")
	}
	options.LookupEnv = func(key string) (string, bool) {
		if key == "TERM_BACKGROUND" {
			return "dark", true
		}
		return "", false
	}
	if !Resolve(options).Renderer.HasDarkBackground() {
		t.Fatal("dark TERM_BACKGROUND was detected as light")
	}
}

func TestASCIIColorProfileDowngradesTTYToPlain(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("COLORTERM", "")
	t.Setenv("CLICOLOR", "0")
	terminal := true
	render := Resolve(ResolveOptions{
		Output: &bytes.Buffer{}, ForceTerminal: &terminal,
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if render.Mode != Plain {
		t.Fatalf("ASCII color profile resolved mode %v, want Plain", render.Mode)
	}
}
