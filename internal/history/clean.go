package history

import (
	"regexp"
	"strings"
)

// Codex envelope fixtures follow the pinned public producer implementation:
// github.com/openai/codex/tree/bd92b056ddd91bd7c2ecfea3d8773f7eb5a879a6/codex-rs/core/src/context.
// Every rule is a complete match.
var localCommandWrapper = regexp.MustCompile(`(?s)^<local-command-caveat>.*</local-command-caveat>\n<command-name>.*</command-name>(?:\n<command-message>.*</command-message>)?(?:\n<command-args>.*</command-args>)?$`)
var commandRecord = regexp.MustCompile(`(?s)^<command-name>[^<]*</command-name>(?:\n ?<command-message>.*</command-message>)?(?:\n ?<command-args>.*</command-args>)?$`)
var teammateMessage = regexp.MustCompile(`(?s)^<teammate-message(?: [A-Za-z_][A-Za-z0-9_-]*="[^"\r\n]*")*>.*</teammate-message>$`)
var agentMessageEnvelope = regexp.MustCompile(`(?s)^Message Type: (?:MESSAGE|FINAL_ANSWER)\nTask name: [^\r\n]+\nSender: [^\r\n]+\nPayload:\n.*$`)
var delegationEnvelope = regexp.MustCompile(`(?s)^Message Type: NEW_TASK\nTask name: [^\r\n]+\nSender: [^\r\n]+\nPayload:\n.*$`)
var bashCommandRecords = regexp.MustCompile(`(?s)^(?:(?:<bash-input>.*?</bash-input>)|(?:<bash-stdout>.*?</bash-stdout>)|(?:<bash-stderr>.*?</bash-stderr>))+$`)

// CleanHumanText applies only format-independent, lossless normalization and
// a complete-wrapper allowlist. Arbitrary XML-like user content is preserved.
func CleanHumanText(value string) (string, Classification, bool, bool) {
	clean := strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
	if clean == "" {
		return "", ClassificationUnknown, false, false
	}

	classification := ClassificationHuman
	searchable := true
	switch {
	case completeTag(clean, "environment_context"), completeTag(clean, "turn_aborted"):
		classification, searchable = ClassificationSystemInjected, false
	case completeTag(clean, "system-reminder"):
		classification, searchable = ClassificationAgentInstruction, false
	case completeAgentInstructions(clean):
		classification, searchable = ClassificationAgentInstruction, false
	case localCommandWrapper.MatchString(clean):
		classification, searchable = ClassificationLocalCommand, false
	}

	if len([]byte(clean)) > MaxPromptBytes {
		return clean, classification, false, true
	}
	return clean, classification, searchable, false
}

// ClassifyPromptKind applies a complete-match allowlist to normalized prompt
// text. Unproven user text remains human; non-user roles stay unknown.
func ClassifyPromptKind(value string, role Role, classification Classification) PromptKind {
	if role != RoleUser {
		return PromptKindUnknown
	}
	clean := strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
	switch {
	case delegationEnvelope.MatchString(clean), completeTag(clean, "codex_delegation"):
		return PromptKindDelegation
	case agentMessageEnvelope.MatchString(clean), teammateMessage.MatchString(clean):
		return PromptKindAgentMessage
	case localCommandWrapper.MatchString(clean), commandRecord.MatchString(clean),
		completeTag(clean, "local-command-stdout"), completeTag(clean, "user_shell_command"),
		bashCommandRecords.MatchString(clean):
		return PromptKindCommand
	case completeTag(clean, "turn_aborted"), completeTag(clean, "subagent_notification"),
		completeTag(clean, "task-notification"), completeTag(clean, "heartbeat"):
		return PromptKindControl
	case classification == ClassificationHuman:
		return PromptKindHuman
	default:
		return PromptKindUnknown
	}
}

func completeAgentInstructions(value string) bool {
	const marker = "\n\n<INSTRUCTIONS>"
	markerIndex := strings.Index(value, marker)
	if markerIndex < 0 || !strings.HasSuffix(value, "</INSTRUCTIONS>") {
		return false
	}
	header := value[:markerIndex]
	if header == "# AGENTS.md instructions" {
		return true
	}
	const pathPrefix = "# AGENTS.md instructions for "
	return strings.HasPrefix(header, pathPrefix) && len(header) > len(pathPrefix) && !strings.ContainsAny(header, "\r\n")
}

func completeTag(value, tag string) bool {
	open, close := "<"+tag+">", "</"+tag+">"
	if !strings.HasPrefix(value, open) || !strings.HasSuffix(value, close) {
		return false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(value, open), close)
	return !strings.Contains(body, open) && !strings.Contains(body, close)
}
