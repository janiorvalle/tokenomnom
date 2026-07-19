// Package theme owns tokenomnom's terminal palette and render-mode decisions.
package theme

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

const defaultWidth = 80

// Mode selects ANSI-styled or byte-stable plain output.
type Mode uint8

const (
	Plain Mode = iota
	Styled
)

type fileDescriptor interface {
	Fd() uintptr
}

// ResolveOptions supplies command state and test-only terminal overrides.
type ResolveOptions struct {
	NoColor          bool
	Color            string
	IgnoreNoColorEnv bool
	Format           string
	Output           io.Writer
	LookupEnv        func(string) (string, bool)
	ForceTerminal    *bool
	Width            int
	ForceColor       bool
	Dark             *bool
}

// Context is the resolved presentation state for one command execution.
type Context struct {
	Mode        Mode
	Interactive bool
	Width       int
	Renderer    *lipgloss.Renderer
	Palette     Palette
}

// Resolve centralizes color, TTY, and terminal-width detection.
func Resolve(options ResolveOptions) Context {
	lookupEnv := options.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}

	width := options.Width
	terminal := false
	fd := -1
	if output, ok := options.Output.(fileDescriptor); ok {
		fd = int(output.Fd())
		terminal = term.IsTerminal(fd)
		if width == 0 {
			if detected, _, err := term.GetSize(fd); err == nil && detected > 0 {
				width = detected
			}
		}
	}
	if options.ForceTerminal != nil {
		terminal = *options.ForceTerminal
	}
	if width <= 0 {
		width = defaultWidth
	}

	mode := Styled
	_, noColorEnv := lookupEnv("NO_COLOR")
	if options.IgnoreNoColorEnv {
		noColorEnv = false
	}
	if options.NoColor || options.Color == "never" || noColorEnv || options.Format == "json" {
		mode = Plain
	} else if options.Color != "always" && !terminal {
		mode = Plain
	}

	renderer := lipgloss.NewRenderer(options.Output)
	if options.ForceColor || options.Color == "always" {
		// termenv.TrueColor is profile value zero. Keeping the override here
		// avoids exposing lipgloss's transitive termenv dependency to callers.
		renderer.SetColorProfile(0)
	}
	if options.Dark != nil {
		renderer.SetHasDarkBackground(*options.Dark)
	} else {
		renderer.SetHasDarkBackground(darkBackground(lookupEnv))
	}
	// termenv.Ascii is profile value three. A TTY without a color profile cannot
	// distinguish provider segments, so it uses the stable Plain presentation.
	if mode == Styled && options.Color != "always" && renderer.ColorProfile() == 3 {
		mode = Plain
	}
	return Context{Mode: mode, Interactive: terminal && mode == Styled, Width: width, Renderer: renderer, Palette: NewPalette(renderer)}
}

func darkBackground(lookupEnv func(string) (string, bool)) bool {
	if value, ok := lookupEnv("TERM_BACKGROUND"); ok {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "light":
			return false
		case "dark":
			return true
		}
	}
	if value, ok := lookupEnv("COLORFGBG"); ok {
		parts := strings.Split(value, ";")
		background, err := strconv.Atoi(parts[len(parts)-1])
		if err == nil {
			return background < 7 || background == 8
		}
	}
	// Dark terminals are the common case, and an explicit default avoids
	// lipgloss's synchronous OSC background query on nonresponsive TTYs.
	return true
}

type contextKey struct{}

// WithContext attaches resolved presentation state to a command context.
func WithContext(parent context.Context, render Context) context.Context {
	return context.WithValue(parent, contextKey{}, render)
}

// FromContext returns Plain presentation state when no state was attached.
func FromContext(ctx context.Context) Context {
	if render, ok := ctx.Value(contextKey{}).(Context); ok {
		return render
	}
	return Context{Mode: Plain, Width: defaultWidth}
}
