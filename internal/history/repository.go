package history

import (
	"net/url"
	"strings"
)

const (
	// RepositoryRuleVersion identifies the deterministic repository URL
	// normalization used by extractors and history-store backfills.
	RepositoryRuleVersion = 1
)

// DeriveRepository normalizes a provider-supplied repository URL and returns
// its final path segment without inferring anything from the working directory.
func DeriveRepository(raw string) (identity, name string) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", ""
	}

	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		identity = parsed.Host + parsed.EscapedPath()
	} else {
		identity = value
		if at := strings.LastIndex(identity, "@"); at >= 0 {
			firstSeparator := strings.IndexAny(identity, "/:")
			if firstSeparator < 0 || at < firstSeparator {
				identity = identity[at+1:]
			}
		}
		if scheme := strings.Index(identity, "://"); scheme >= 0 {
			identity = identity[scheme+3:]
		}
		if cut := strings.IndexAny(identity, "?#"); cut >= 0 {
			identity = identity[:cut]
		}
	}

	identity = strings.TrimRight(identity, "/")
	if len(identity) >= 4 && strings.EqualFold(identity[len(identity)-4:], ".git") {
		identity = identity[:len(identity)-4]
	}
	identity = strings.TrimRight(identity, "/")
	if identity == "" {
		return "", ""
	}

	separator := strings.LastIndex(identity, "/")
	if separator < 0 {
		separator = strings.LastIndex(identity, ":")
	}
	name = identity[separator+1:]
	if name == "" {
		return "", ""
	}
	return identity, name
}
