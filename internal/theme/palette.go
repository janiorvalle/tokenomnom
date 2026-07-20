package theme

import "github.com/charmbracelet/lipgloss"

// Palette contains all terminal colors used by reports and future visualizations.
type Palette struct {
	renderer *lipgloss.Renderer

	codex   []lipgloss.AdaptiveColor
	claude  []lipgloss.AdaptiveColor
	heatmap []lipgloss.AdaptiveColor

	headerColor   lipgloss.AdaptiveColor
	subtleColor   lipgloss.AdaptiveColor
	emphasisColor lipgloss.AdaptiveColor
	warningColor  lipgloss.AdaptiveColor
	moneyColor    lipgloss.AdaptiveColor
	borderColor   lipgloss.AdaptiveColor
	successColor  lipgloss.AdaptiveColor
}

// NewPalette builds adaptive styles for light and dark terminal backgrounds.
func NewPalette(renderer *lipgloss.Renderer) Palette {
	return Palette{
		renderer: renderer,
		codex: []lipgloss.AdaptiveColor{
			{Light: "#006D77", Dark: "#5EEAD4"},
			{Light: "#087F8C", Dark: "#2DD4BF"},
			{Light: "#0E7490", Dark: "#22D3EE"},
			{Light: "#155E75", Dark: "#67E8F9"},
			{Light: "#115E59", Dark: "#99F6E4"},
		},
		claude: []lipgloss.AdaptiveColor{
			{Light: "#B54708", Dark: "#FDBA74"},
			{Light: "#C2410C", Dark: "#FB923C"},
			{Light: "#A16207", Dark: "#FACC15"},
			{Light: "#9A3412", Dark: "#FED7AA"},
			{Light: "#92400E", Dark: "#FDE68A"},
		},
		heatmap: []lipgloss.AdaptiveColor{
			{Light: "#E5E7EB", Dark: "#26323A"},
			{Light: "#A7F3D0", Dark: "#134E4A"},
			{Light: "#5EEAD4", Dark: "#0F766E"},
			{Light: "#14B8A6", Dark: "#14B8A6"},
			{Light: "#0F766E", Dark: "#5EEAD4"},
		},
		headerColor:   lipgloss.AdaptiveColor{Light: "#1F2937", Dark: "#F3F4F6"},
		subtleColor:   lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"},
		emphasisColor: lipgloss.AdaptiveColor{Light: "#4338CA", Dark: "#A5B4FC"},
		warningColor:  lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"},
		moneyColor:    lipgloss.AdaptiveColor{Light: "#047857", Dark: "#6EE7B7"},
		borderColor:   lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#374151"},
		successColor:  lipgloss.AdaptiveColor{Light: "#047857", Dark: "#34D399"},
	}
}

func (p Palette) style() lipgloss.Style {
	if p.renderer == nil {
		return lipgloss.NewStyle()
	}
	return p.renderer.NewStyle()
}

func (p Palette) adaptiveColor(color lipgloss.AdaptiveColor) lipgloss.Color {
	if p.renderer != nil && p.renderer.HasDarkBackground() {
		return lipgloss.Color(color.Dark)
	}
	return lipgloss.Color(color.Light)
}

// Header styles report titles and table headers.
func (p Palette) Header() lipgloss.Style {
	return p.style().Bold(true).Foreground(p.adaptiveColor(p.headerColor))
}

// Subtle styles supporting metadata and explanatory copy.
func (p Palette) Subtle() lipgloss.Style {
	return p.style().Foreground(p.adaptiveColor(p.subtleColor))
}

// Emphasis styles important non-monetary values.
func (p Palette) Emphasis() lipgloss.Style {
	return p.style().Bold(true).Foreground(p.adaptiveColor(p.emphasisColor))
}

// Warning styles incomplete-pricing and discovery notes.
func (p Palette) Warning() lipgloss.Style {
	return p.style().Bold(true).Foreground(p.adaptiveColor(p.warningColor))
}

// Money styles cost values.
func (p Palette) Money() lipgloss.Style {
	return p.style().Foreground(p.adaptiveColor(p.moneyColor))
}

// Border styles structural chrome: card borders, dividers, rules.
func (p Palette) Border() lipgloss.Style {
	return p.style().Foreground(p.adaptiveColor(p.borderColor))
}

// BorderColor exposes the chrome color for lipgloss BorderForeground.
func (p Palette) BorderColor() lipgloss.Color {
	return p.adaptiveColor(p.borderColor)
}

// AccentBorderColor exposes the emphasis color for focused chrome.
func (p Palette) AccentBorderColor() lipgloss.Color {
	return p.adaptiveColor(p.emphasisColor)
}

// Success styles completed-state markers such as installed and synced.
func (p Palette) Success() lipgloss.Style {
	return p.style().Foreground(p.adaptiveColor(p.successColor))
}

// ProviderColor maps a provider and model rank to a deterministic ramp shade.
func (p Palette) ProviderColor(provider string, rank int) lipgloss.AdaptiveColor {
	ramp := p.codex
	if provider == "claude" || provider == "Claude" {
		ramp = p.claude
	}
	if rank < 0 {
		rank = 0
	}
	return ramp[rank%len(ramp)]
}

// Provider styles a provider or one of its ranked models.
func (p Palette) Provider(provider string, rank int) lipgloss.Style {
	return p.style().Foreground(p.adaptiveColor(p.ProviderColor(provider, rank)))
}

// Badge styles pricing provenance markers.
func (p Palette) Badge(status string) lipgloss.Style {
	switch status {
	case "proxy":
		return p.style().Bold(true).
			Foreground(p.adaptiveColor(lipgloss.AdaptiveColor{Light: "#7C2D12", Dark: "#FFEDD5"})).
			Background(p.adaptiveColor(lipgloss.AdaptiveColor{Light: "#FED7AA", Dark: "#9A3412"}))
	case "estimated":
		return p.style().Bold(true).
			Foreground(p.adaptiveColor(lipgloss.AdaptiveColor{Light: "#713F12", Dark: "#FEF3C7"})).
			Background(p.adaptiveColor(lipgloss.AdaptiveColor{Light: "#FDE68A", Dark: "#854D0E"}))
	case "override":
		return p.style().Bold(true).
			Foreground(p.adaptiveColor(lipgloss.AdaptiveColor{Light: "#312E81", Dark: "#EEF2FF"})).
			Background(p.adaptiveColor(lipgloss.AdaptiveColor{Light: "#C7D2FE", Dark: "#4338CA"}))
	default:
		return p.Emphasis()
	}
}

// HeatmapColor returns one of the shared five contribution-intensity colors.
func (p Palette) HeatmapColor(index int) lipgloss.AdaptiveColor {
	if index < 0 {
		index = 0
	}
	if index >= len(p.heatmap) {
		index = len(p.heatmap) - 1
	}
	return p.heatmap[index]
}

// Heatmap styles one contribution-intensity cell.
func (p Palette) Heatmap(index int) lipgloss.Style {
	return p.style().Foreground(p.adaptiveColor(p.HeatmapColor(index)))
}
