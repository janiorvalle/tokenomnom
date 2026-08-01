package pages

import historystore "github.com/janiorvalle/tokenomnom/internal/history/store"

// SearchHit is the presentation-ready shape for one indexed prompt match.
type SearchHit struct {
	PromptID          string
	SessionID         string
	Provider          string
	Date              string
	Project           string
	Snippet           string
	SnippetMatchStart string
	SnippetMatchEnd   string
}

// SearchResult contains one bounded search response.
type SearchResult struct {
	Hits     []SearchHit
	HasMore  bool
	Warnings []string
}

// SessionPrompt is one prompt preview in the selected session.
type SessionPrompt struct {
	PromptID string
	Date     string
	Snippet  string
}

// SessionDetail is the presentation-ready session view opened from a result.
type SessionDetail struct {
	CatalogSession  historystore.CatalogSession
	SessionID       string
	Provider        string
	Project         string
	ProjectSource   string
	FirstDate       string
	LastDate        string
	Preview         string
	PromptCount     int
	OccurrenceCount int
	HasMore         bool
	Prompts         []SessionPrompt
}

// HistorySearchData is returned by the CLI-owned history loader.
type HistorySearchData struct {
	NotIndexed bool
	Search     SearchResult
	Session    *SessionDetail
}
