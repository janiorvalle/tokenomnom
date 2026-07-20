package history

import (
	"regexp"
	"strings"
)

var localCommandWrapper = regexp.MustCompile(`(?s)^<local-command-caveat>.*</local-command-caveat>\n<command-name>.*</command-name>(?:\n<command-message>.*</command-message>)?(?:\n<command-args>.*</command-args>)?$`)

// CleanHumanText applies only format-independent, lossless normalization and
// a complete-wrapper allowlist. Arbitrary XML-like user content is preserved.
func CleanHumanText(value string) (string, Classification, bool, bool) {
	clean := strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
	if clean == "" {
		return "", ClassificationUnknown, false, false
	}

	switch {
	case completeTag(clean, "environment_context"), completeTag(clean, "turn_aborted"):
		return clean, ClassificationSystemInjected, false, false
	case completeTag(clean, "system-reminder"):
		return clean, ClassificationAgentInstruction, false, false
	case completeAgentInstructions(clean):
		return clean, ClassificationAgentInstruction, false, false
	case localCommandWrapper.MatchString(clean):
		return clean, ClassificationLocalCommand, false, false
	}

	if len([]byte(clean)) > MaxPromptBytes {
		return clean, ClassificationHuman, false, true
	}
	return clean, ClassificationHuman, true, false
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
