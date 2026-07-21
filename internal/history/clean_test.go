package history

import (
	"strings"
	"testing"
	"time"
)

func TestCleanHumanTextConservative(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		want           string
		classification Classification
		searchable     bool
	}{
		{"normalizes edges", "  hello\r\n```go\r\nx := 1\r\n```  ", "hello\n```go\nx := 1\n```", ClassificationHuman, true},
		{"preserves arbitrary XML", "<request>keep me</request>", "<request>keep me</request>", ClassificationHuman, true},
		{"environment", "<environment_context>private</environment_context>", "<environment_context>private</environment_context>", ClassificationSystemInjected, false},
		{"multiple wrappers remain human", "<environment_context>one</environment_context> question <environment_context>two</environment_context>", "<environment_context>one</environment_context> question <environment_context>two</environment_context>", ClassificationHuman, true},
		{"local command", "<local-command-caveat>generated</local-command-caveat>\n<command-name>/review</command-name>\n<command-args></command-args>", "<local-command-caveat>generated</local-command-caveat>\n<command-name>/review</command-name>\n<command-args></command-args>", ClassificationLocalCommand, false},
		{"path agents instructions", "# AGENTS.md instructions for /workspace/demo\n\n<INSTRUCTIONS>generated</INSTRUCTIONS>", "# AGENTS.md instructions for /workspace/demo\n\n<INSTRUCTIONS>generated</INSTRUCTIONS>", ClassificationAgentInstruction, false},
		{"multiline agents-like heading", "# AGENTS.md instructions for /workspace/demo\nadditional user context\n\n<INSTRUCTIONS>keep this</INSTRUCTIONS>", "# AGENTS.md instructions for /workspace/demo\nadditional user context\n\n<INSTRUCTIONS>keep this</INSTRUCTIONS>", ClassificationHuman, true},
		{"similar human heading", "# AGENTS.md instructions about testing\n\n<INSTRUCTIONS>keep this</INSTRUCTIONS>", "# AGENTS.md instructions about testing\n\n<INSTRUCTIONS>keep this</INSTRUCTIONS>", ClassificationHuman, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, classification, searchable, oversized := CleanHumanText(test.input)
			if got != test.want || classification != test.classification || searchable != test.searchable || oversized {
				t.Fatalf("got (%q, %q, %v, %v)", got, classification, searchable, oversized)
			}
		})
	}
}

func TestCleanHumanTextMarksOversizedWithoutTruncating(t *testing.T) {
	input := strings.Repeat("x", MaxPromptBytes+1)
	got, classification, searchable, oversized := CleanHumanText(input)
	if got != input || classification != ClassificationHuman || searchable || !oversized {
		t.Fatalf("oversized prompt was not retained and excluded")
	}
}

func TestCanonicalPromptPrefersLaterOversizedHumanVariant(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	draft := Prompt{CleanText: "draft", Classification: ClassificationHuman, Searchable: true, Timestamp: &base}
	later := base.Add(time.Minute)
	final := Prompt{CleanText: strings.Repeat("x", MaxPromptBytes+1), Classification: ClassificationHuman, Oversized: true, Timestamp: &later}
	if !CanonicalPromptWins(final, draft) {
		t.Fatal("later oversized final prompt lost to searchable draft")
	}
}
