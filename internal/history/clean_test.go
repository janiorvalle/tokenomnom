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
	command := "<local-command-caveat>" + strings.Repeat("x", MaxPromptBytes+1) + "</local-command-caveat>\n<command-name>/review</command-name>"
	got, classification, searchable, oversized = CleanHumanText(command)
	if got != command || classification != ClassificationLocalCommand || searchable || !oversized {
		t.Fatalf("oversized command envelope was not retained and excluded")
	}
}

func TestClassifyPromptKindCompleteMatchAllowlist(t *testing.T) {
	tests := []struct {
		name string
		text string
		want PromptKind
	}{
		{"human", "Please inspect <task-notification>this text</task-notification> carefully.", PromptKindHuman},
		{"delegation", "<codex_delegation>\n<task>Review the parser.</task>\n</codex_delegation>", PromptKindDelegation},
		{"delegation lookalike", "prefix <codex_delegation>task</codex_delegation>", PromptKindHuman},
		{"new task", "Message Type: NEW_TASK\nTask name: /root/parser\nSender: /root\nPayload:\nReview the parser.", PromptKindDelegation},
		{"agent message", "Message Type: MESSAGE\nTask name: /root\nSender: /root/parser\nPayload:\nParser review complete.", PromptKindAgentMessage},
		{"teammate", "<teammate-message teammate_id=\"parser\" color=\"blue\">done</teammate-message>", PromptKindAgentMessage},
		{"harness teammate", teammateMessagePreamble + "<teammate-message teammate_id=\"parser\" color=\"blue\">done</teammate-message>", PromptKindAgentMessage},
		{"harness teammate multiline attributes", teammateMessagePreamble + "<teammate-message\n  teammate_id=\"parser\"\n  color=\"blue\">done</teammate-message>", PromptKindAgentMessage},
		{"harness teammate with trailer", teammateMessagePreamble + "<teammate-message teammate_id=\"parser\">done</teammate-message>\n\n" + teammateMessageTrailer, PromptKindAgentMessage},
		{"harness teammate leading prose", "Please review this:\n" + teammateMessagePreamble + "<teammate-message teammate_id=\"parser\">done</teammate-message>", PromptKindHuman},
		{"harness teammate trailing prose", teammateMessagePreamble + "<teammate-message teammate_id=\"parser\">done</teammate-message>\nThanks", PromptKindHuman},
		{"harness teammate sibling follow-up", teammateMessagePreamble + "<teammate-message teammate_id=\"parser\">done</teammate-message>\nhuman follow-up\n<teammate-message teammate_id=\"parser\">more</teammate-message>", PromptKindHuman},
		{"harness teammate code fence", "```text\n" + teammateMessagePreamble + "<teammate-message teammate_id=\"parser\">done</teammate-message>\n```", PromptKindHuman},
		{"teammate malformed", "<teammate-message teammate_id=parser>done</teammate-message>", PromptKindHuman},
		{"slash record", "<command-name>/model</command-name>\n            <command-message>model</command-message>\n            <command-args></command-args>", PromptKindCommand},
		{"slash record without args", "<command-name>/help</command-name>\n<command-message>help</command-message>", PromptKindCommand},
		{"slash record with stdout", "<command-name>/model</command-name>\n<command-message>model</command-message>\n<command-args>sonnet</command-args>\n<local-command-stdout>changed</local-command-stdout>", PromptKindCommand},
		{"slash record with caveat", "<local-command-caveat>generated locally</local-command-caveat>\n<command-name>/clear</command-name>\n    <command-message>clear</command-message>\n    <command-args></command-args>\n<local-command-stdout></local-command-stdout>", PromptKindCommand},
		{"custom command message first", "<command-message>review is running</command-message>\n<command-name>/review</command-name>", PromptKindCommand},
		{"custom command message first with args", "<command-message>review is running</command-message>\n<command-name>/review</command-name>\n<command-args>--base main\n--brief</command-args>", PromptKindCommand},
		{"custom command discussed before envelope", "Earlier output was:\n<command-message>review is running</command-message>\n<command-name>/review</command-name>", PromptKindHuman},
		{"custom command discussed after envelope", "<command-message>review is running</command-message>\n<command-name>/review</command-name>\nThat command produced the result above.", PromptKindHuman},
		{"command caveat", "<local-command-caveat>generated locally</local-command-caveat>", PromptKindCommand},
		{"command output", "<local-command-stdout>Model changed.</local-command-stdout>", PromptKindCommand},
		{"slash prose", "/etc/hosts is broken", PromptKindHuman},
		{"slash tag mentioned in prose", "The transcript contains <command-message>help</command-message> here.", PromptKindHuman},
		{"slash record trailing prose", "<command-name>/help</command-name>\n<command-message>help</command-message>\nplease continue", PromptKindHuman},
		{"shell command", "<user_shell_command>\n<command>git status</command>\n</user_shell_command>", PromptKindCommand},
		{"bash records", "<bash-input>git status</bash-input><bash-stdout>clean</bash-stdout><bash-stderr></bash-stderr>", PromptKindCommand},
		{"bash mismatched tags", "<bash-input>human text</bash-stderr>", PromptKindHuman},
		{"control", "<task-notification>background task finished</task-notification>", PromptKindControl},
		{"control lookalike", "<task-notification>done</task-notification> thanks", PromptKindHuman},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyPromptKind(test.text, RoleUser, ClassificationHuman); got != test.want {
				t.Fatalf("kind = %q, want %q", got, test.want)
			}
		})
	}
	if got := ClassifyPromptKind("assistant text", RoleAssistant, ClassificationAssistant); got != PromptKindUnknown {
		t.Fatalf("assistant kind = %q", got)
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
