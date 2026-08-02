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

// SessionPromptPage is the bounded prompt list used by a session detail view.
type SessionPromptPage struct {
	Prompts []SessionPrompt
	HasMore bool
	Warning string
}

// SessionModel is one model/provider split from history cost attribution.
// The fields stay presentation-ready so the TUI does not need pricing or
// transcript access while it renders a detail view.
type SessionModel struct {
	Date           string
	Provider       string
	Model          string
	InputTokens    int64
	CacheTokens    int64
	OutputTokens   int64
	TotalTokens    int64
	CostUSD        float64
	PricedTokens   int64
	UnpricedTokens int64
}

// SessionCost is the optional #114 attribution snapshot for one session.
// Empty values are rendered as an explicit unavailable state rather than as
// fabricated zeros.
type SessionCost struct {
	Status         string
	TotalTokens    int64
	PricedTokens   int64
	UnpricedTokens int64
	CostUSD        float64
	Models         []SessionModel
}

// SearchPreview contains the selected hit's session context. The detail is
// shared with the full session view; PromptID tells the renderer which row to
// center in the bounded context list.
type SearchPreview struct {
	PromptID string
	Detail   *SessionDetail
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
	Cost            SessionCost
}

// HistorySearchData is returned by the CLI-owned history loader.
type HistorySearchData struct {
	NotIndexed bool
	Search     SearchResult
	Session    *SessionDetail
	Preview    *SearchPreview
}
