package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/ingest"
	claudeingest "github.com/janiorvalle/tokenomnom/internal/ingest/claude"
	codexingest "github.com/janiorvalle/tokenomnom/internal/ingest/codex"
	pricinglib "github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

const maxHistorySessionCostModels = 32

type historySessionCostFlags struct {
	provider   string
	since      string
	until      string
	cwd        string
	repo       string
	project    string
	branch     string
	source     string
	limit      int
	cursor     string
	threadKind string
	rootOnly   bool
}

func (flags *historySessionCostFlags) add(command *cobra.Command) {
	command.Flags().StringVar(&flags.provider, "provider", "", "filter by provider (codex or claude)")
	command.Flags().StringVar(&flags.since, "since", "", "include sessions active on or after YYYY-MM-DD")
	command.Flags().StringVar(&flags.until, "until", "", "include sessions active on or before YYYY-MM-DD")
	command.Flags().StringVar(&flags.cwd, "cwd", "", "filter by exact working directory")
	command.Flags().StringVar(&flags.repo, "repo", "", "filter by known repository name")
	command.Flags().StringVar(&flags.project, "project", "", "filter by derived project name")
	command.Flags().StringVar(&flags.branch, "branch", "", "filter by known branch")
	command.Flags().StringVar(&flags.source, "source", "any", "filter by availability source")
	command.Flags().IntVar(&flags.limit, "limit", historystore.MaxSessionCostPageSize, "maximum page rows (1-100)")
	command.Flags().StringVar(&flags.cursor, "cursor", "", "continue a previous page")
	command.Flags().StringVar(&flags.threadKind, "thread-kind", "all", "filter by thread kind (root, subagent, unknown, or all)")
	command.Flags().BoolVar(&flags.rootOnly, "root-only", false, "include only directly evidenced root sessions")
}

func (flags historySessionCostFlags) validate(command *cobra.Command) error {
	if _, err := historyProviders(flags.provider); err != nil {
		return err
	}
	if err := validateDateFlag("since", flags.since); err != nil {
		return err
	}
	if err := validateDateFlag("until", flags.until); err != nil {
		return err
	}
	if flags.since != "" && flags.until != "" && flags.until < flags.since {
		return errors.New("--until must be on or after --since")
	}
	if flags.source != "any" && flags.source != "provider" && flags.source != "provider-live" && flags.source != "provider-archive" && flags.source != "vault" {
		return fmt.Errorf("invalid --source %q (expected any, provider, provider-live, provider-archive, or vault)", flags.source)
	}
	if flags.rootOnly && command.Flags().Changed("thread-kind") {
		return errors.New("--root-only and --thread-kind are mutually exclusive")
	}
	if flags.threadKind != "all" && flags.threadKind != "root" && flags.threadKind != "subagent" && flags.threadKind != "unknown" {
		return fmt.Errorf("invalid --thread-kind %q (expected root, subagent, unknown, or all)", flags.threadKind)
	}
	if flags.limit < 1 || flags.limit > historystore.MaxSessionCostPageSize {
		return fmt.Errorf("--limit must be between 1 and %d", historystore.MaxSessionCostPageSize)
	}
	return nil
}

func (flags historySessionCostFlags) hasExplicitListOption(command *cobra.Command) bool {
	for _, name := range []string{"provider", "since", "until", "cwd", "repo", "project", "branch", "source", "limit", "cursor", "thread-kind", "root-only"} {
		if command.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func (flags historySessionCostFlags) query(command *cobra.Command) historystore.CatalogQuery {
	limit := flags.limit
	if flags.cursor != "" && !command.Flags().Changed("limit") {
		limit = 0
	}
	threadKind := flags.threadKind
	if flags.rootOnly {
		threadKind = "root"
	}
	query := historystore.CatalogQuery{
		Provider:   history.Provider(flags.provider),
		CWD:        flags.cwd,
		Repo:       flags.repo,
		Project:    flags.project,
		Branch:     flags.branch,
		Source:     historystore.CatalogSource(flags.source),
		ThreadKind: threadKind,
		Limit:      limit,
		Cursor:     flags.cursor,
	}
	if flags.since != "" {
		value, _ := time.Parse("2006-01-02", flags.since)
		query.Since = &value
	}
	if flags.until != "" {
		value, _ := time.Parse("2006-01-02", flags.until)
		value = value.Add(24*time.Hour - time.Nanosecond)
		query.Until = &value
	}
	return query
}

func (flags historySessionCostFlags) jsonFilters(page historystore.PageMetadata) jsonFilters {
	threadKind := flags.threadKind
	if flags.rootOnly {
		threadKind = "root"
	}
	limit := page.Limit
	return jsonFilters{
		Provider:   optionalString(flags.provider),
		Since:      optionalString(flags.since),
		Until:      optionalString(flags.until),
		CWD:        optionalString(flags.cwd),
		Repo:       optionalString(flags.repo),
		Project:    optionalString(flags.project),
		Branch:     optionalString(flags.branch),
		Source:     optionalString(flags.source),
		Cursor:     optionalString(flags.cursor),
		Limit:      &limit,
		ThreadKind: optionalString(threadKind),
	}
}

func newHistoryCostsCommand(codexDir, claudeDir *string) *cobra.Command {
	var flags historySessionCostFlags
	command := &cobra.Command{
		Use:     "costs [session-id]",
		Aliases: []string{"cost"},
		Short:   "Price indexed sessions from exact transcript usage",
		Args:    cobra.MaximumNArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.validate(cmd); err != nil {
				return err
			}
			if len(args) == 1 && flags.hasExplicitListOption(cmd) {
				return errors.New("a session ID cannot be combined with history cost list filters; omit the ID or the filters")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			table, err := loadPricingTable()
			if err != nil {
				return err
			}
			var page historystore.SessionCostPage
			query := historystore.SessionCostQuery{Catalog: flags.query(cmd)}
			if len(args) == 1 {
				query.SessionID = args[0]
			}
			if err := withHistoryStore(cmd, func(database *historystore.Store) error {
				var err error
				page, err = database.ListSessionCostSources(query)
				return err
			}); err != nil {
				return err
			}

			location, _ := historyPresentationTimezone(cmd)
			data := historySessionCostData{
				Sessions:   make([]historySessionCostRow, 0, len(page.Sessions)),
				Page:       page.Page,
				Coverage:   page.Coverage,
				Generation: page.Generation,
				Bounds: historySessionCostBounds{
					MaxSessionsPerPage:                historystore.MaxSessionCostPageSize,
					MaxTranscriptCandidatesPerSession: historystore.MaxSessionCostCandidates,
					MaxModelRowsPerSession:            maxHistorySessionCostModels,
					NapkinMath:                        fmt.Sprintf("1,200 sessions / %d max rows = 12 pages; each page parses at most %d preferred transcript locations", historystore.MaxSessionCostPageSize, historystore.MaxSessionCostPageSize*historystore.MaxSessionCostCandidates),
				},
			}
			warnings := append([]string{}, page.Warnings...)
			for _, session := range page.Sessions {
				presentHistorySession(&session.CatalogSession, location)
				row, err := priceHistorySession(cmd, session, table, *codexDir, *claudeDir)
				if err != nil {
					return err
				}
				data.Sessions = append(data.Sessions, row)
				for _, warning := range row.Warnings {
					warnings = append(warnings, fmt.Sprintf("session %s: %s", row.SessionID, warning))
				}
			}
			if currentFormat(cmd) == "json" {
				return writeHistoryJSONEnvelope(cmd, "history costs", flags.jsonFilters(data.Page), warnings, data)
			}
			writeHistorySessionCosts(cmd, data)
			writeHistoryWarnings(cmd, warnings)
			writeHistoryContinuation(cmd, data.Page)
			writeSubtleLine(cmd, pricingDisclaimer)
			return nil
		},
	}
	flags.add(command)
	return command
}

type historySessionCostData struct {
	Sessions   []historySessionCostRow      `json:"sessions"`
	Page       historystore.PageMetadata    `json:"page"`
	Coverage   historystore.CatalogCoverage `json:"coverage"`
	Generation int64                        `json:"index_generation"`
	Bounds     historySessionCostBounds     `json:"bounds"`
}

type historySessionCostBounds struct {
	MaxSessionsPerPage                int    `json:"max_sessions_per_page"`
	MaxTranscriptCandidatesPerSession int    `json:"max_transcript_candidates_per_session"`
	MaxModelRowsPerSession            int    `json:"max_model_rows_per_session"`
	NapkinMath                        string `json:"napkin_math"`
}

type historySessionCostRow struct {
	historystore.CatalogSession
	Tokens            historySessionCostTokens  `json:"tokens"`
	Models            []historySessionCostModel `json:"models"`
	AttributionStatus string                    `json:"attribution_status"`
	TokenSource       string                    `json:"token_source"`
	RawLocationKind   string                    `json:"raw_location_kind,omitempty"`
	Warnings          []string                  `json:"warnings"`
}

type historySessionCostModel struct {
	Date     string            `json:"date"`
	Provider discover.Provider `json:"provider"`
	Model    string            `json:"model"`
	historySessionCostTokens
}

type historySessionCostTokens struct {
	InputTokens                  int64   `json:"input_tokens"`
	CacheReadTokens              int64   `json:"cache_read_tokens"`
	CacheWrite5mTokens           int64   `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens           int64   `json:"cache_write_1h_tokens"`
	CacheWriteUnclassifiedTokens int64   `json:"cache_write_unclassified_tokens"`
	CacheWriteTokens             int64   `json:"cache_write_tokens"`
	OutputTokens                 int64   `json:"output_tokens"`
	ReasoningTokens              int64   `json:"reasoning_tokens"`
	TotalTokens                  int64   `json:"total_tokens"`
	CostUSD                      float64 `json:"cost_usd"`
	PricedTokens                 int64   `json:"priced_tokens"`
	UnpricedTokens               int64   `json:"unpriced_tokens"`
	UnknownModelTokens           int64   `json:"unknown_model_tokens"`
}

func priceHistorySession(cmd *cobra.Command, session historystore.SessionCostSession, table pricinglib.Table, codexDir, claudeDir string) (historySessionCostRow, error) {
	row := historySessionCostRow{
		CatalogSession: session.CatalogSession,
		Models:         []historySessionCostModel{},
		TokenSource:    "indexed_exact_transcript",
		Warnings:       []string{},
	}
	if session.CandidatesTruncated {
		row.Warnings = append(row.Warnings, fmt.Sprintf("only the preferred first %d of %d exact transcript locations were considered", len(session.Candidates), session.CandidateCount))
	}
	if len(session.Candidates) == 0 {
		row.AttributionStatus = "unavailable"
		row.Warnings = append(row.Warnings, "no readable exact transcript bytes were available; restore the source or vault snapshot and rerun `tokenomnom history index`")
		return row, nil
	}

	var events []ingest.UsageEvent
	var selectedKind string
	fallbackUsed := false
	for candidateIndex, candidate := range session.Candidates {
		staged, err := readHistoryRawCandidate(cmd, candidate, codexDir, claudeDir)
		if err != nil {
			continue
		}
		parsed, parseErr := parseHistoryUsageFile(staged.path, session.Provider, candidate.Kind)
		staged.cleanup()
		if parseErr != nil {
			continue
		}
		events = parsed
		selectedKind = candidate.Kind
		fallbackUsed = candidateIndex > 0
		break
	}
	if selectedKind == "" {
		row.AttributionStatus = "unavailable"
		row.Warnings = append(row.Warnings, "indexed transcript bytes could not be read or parsed; restore the source or vault snapshot and rerun `tokenomnom history index`")
		return row, nil
	}
	row.RawLocationKind = selectedKind
	if fallbackUsed {
		row.Warnings = append(row.Warnings, "the preferred exact transcript location was unavailable; cost uses a fallback indexed location and may omit newer usage; restore the source and rerun `tokenomnom history index`")
	}
	usageRows, unknownDateRows, usageWarnings := aggregateHistoryUsage(events, session.Provider)
	row.Warnings = append(row.Warnings, usageWarnings...)
	if len(usageRows) == 0 && len(unknownDateRows) == 0 {
		row.AttributionStatus = "no_usage_events"
		if fallbackUsed {
			row.AttributionStatus = "incomplete"
		}
		row.Warnings = append(row.Warnings, "the exact transcript was read but contained no token-usage records; no cost was calculated")
		return row, nil
	}

	breakdown := table.CostRows(usageRows)
	row.Tokens = historySessionCostTokenTotals(usageRows, unknownDateRows, breakdown)
	row.Models = historySessionCostModels(usageRows, unknownDateRows, table, &row.Warnings)
	row.AttributionStatus = "complete"
	if fallbackUsed {
		row.AttributionStatus = "incomplete"
	}
	if len(unknownDateRows) > 0 {
		row.AttributionStatus = "incomplete"
	}
	if row.Tokens.UnpricedTokens > 0 {
		row.Warnings = append(row.Warnings, fmt.Sprintf("%d tokens could not be priced; add a matching model entry to the pricing override or restore a timestamp", row.Tokens.UnpricedTokens))
	}
	if breakdown.UnclassifiedCacheWriteTokens > 0 {
		row.Warnings = append(row.Warnings, fmt.Sprintf("%d unclassified cache-write tokens use the model 1h cache-write pricing policy", breakdown.UnclassifiedCacheWriteTokens))
	}
	if row.Tokens.UnknownModelTokens > 0 {
		row.Warnings = append(row.Warnings, fmt.Sprintf("%d tokens came from an unknown model; re-index after model metadata is available", row.Tokens.UnknownModelTokens))
	}
	return row, nil
}

func parseHistoryUsageFile(path string, provider history.Provider, candidateKind string) ([]ingest.UsageEvent, error) {
	var events []ingest.UsageEvent
	source := discover.SourceFile{
		Provider: discover.Provider(provider),
		Kind:     historySourceFileKind(provider, candidateKind),
		Path:     path,
	}
	switch provider {
	case history.ProviderCodex:
		_, err := (codexingest.Adapter{}).ParseFile(source, func(event ingest.UsageEvent) { events = append(events, event) })
		return events, err
	case history.ProviderClaude:
		deduper := claudeingest.NewDeduper()
		_, err := (claudeingest.Adapter{}).ParseFile(source, deduper.Add)
		if err != nil {
			return nil, err
		}
		deduper.Emit(func(event ingest.UsageEvent) { events = append(events, event) })
		return events, nil
	default:
		return nil, fmt.Errorf("unsupported history provider %q", provider)
	}
}

func historySourceFileKind(provider history.Provider, candidateKind string) discover.SourceKind {
	if candidateKind == "provider_archive" {
		return discover.SourceCodexArchive
	}
	if provider == history.ProviderClaude {
		return discover.SourceClaudeProject
	}
	return discover.SourceCodexLive
}

type historyUsageKey struct {
	Date     string
	Provider discover.Provider
	Model    string
}

func aggregateHistoryUsage(events []ingest.UsageEvent, sessionProvider history.Provider) ([]store.Usage, []store.Usage, []string) {
	knownRows := map[historyUsageKey]store.Usage{}
	unknownDateRows := map[historyUsageKey]store.Usage{}
	warnings := []string{}
	missingTimestampCount := 0
	missingTimestampTokens := int64(0)
	for _, event := range events {
		provider := event.Provider
		if provider == "" {
			provider = discover.Provider(sessionProvider)
		}
		model := strings.TrimSpace(event.Model)
		if model == "" {
			model = "unknown"
		}
		if event.Timestamp.IsZero() {
			missingTimestampCount++
			missingTimestampTokens += event.Input + event.Output
			accumulateHistoryUsage(unknownDateRows, event, provider, model, "")
			continue
		}
		// Pricing effective dates are canonical UTC dates; --tz only affects
		// displayed history timestamps and never changes money.
		date := event.Timestamp.UTC().Format("2006-01-02")
		accumulateHistoryUsage(knownRows, event, provider, model, date)
	}
	if missingTimestampCount > 0 {
		warnings = append(warnings, fmt.Sprintf("%d token-usage records had no timestamp; %d tokens remain visible as unpriced unknown-date usage", missingTimestampCount, missingTimestampTokens))
	}
	return sortedHistoryUsageRows(knownRows), sortedHistoryUsageRows(unknownDateRows), warnings
}

func accumulateHistoryUsage(rows map[historyUsageKey]store.Usage, event ingest.UsageEvent, provider discover.Provider, model, date string) {
	key := historyUsageKey{Date: date, Provider: provider, Model: model}
	row := rows[key]
	row.Date, row.Provider, row.Model = key.Date, key.Provider, key.Model
	row.Input += event.Input
	row.CacheRead += event.CacheRead
	row.CacheWrite5m += event.CacheWrite5m
	row.CacheWrite1h += event.CacheWrite1h
	row.CacheWriteUnclassified += event.CacheWriteUnclassified
	row.Output += event.Output
	row.Reasoning += event.Reasoning
	rows[key] = row
}

func sortedHistoryUsageRows(rows map[historyUsageKey]store.Usage) []store.Usage {
	result := make([]store.Usage, 0, len(rows))
	for _, row := range rows {
		result = append(result, row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Date != result[j].Date {
			return result[i].Date < result[j].Date
		}
		if result[i].Provider != result[j].Provider {
			return result[i].Provider < result[j].Provider
		}
		return result[i].Model < result[j].Model
	})
	return result
}

func historySessionCostTokenTotals(rows, unknownDateRows []store.Usage, breakdown pricinglib.CostBreakdown) historySessionCostTokens {
	var totals historySessionCostTokens
	addTokens := func(row store.Usage) {
		totals.InputTokens += row.Input
		totals.CacheReadTokens += row.CacheRead
		totals.CacheWrite5mTokens += row.CacheWrite5m
		totals.CacheWrite1hTokens += row.CacheWrite1h
		totals.CacheWriteUnclassifiedTokens += row.CacheWriteUnclassified
		totals.CacheWriteTokens += row.CacheWrite5m + row.CacheWrite1h + row.CacheWriteUnclassified
		totals.OutputTokens += row.Output
		totals.ReasoningTokens += row.Reasoning
		totals.TotalTokens += row.Input + row.Output
		if row.Model == "unknown" {
			totals.UnknownModelTokens += row.Input + row.Output
		}
	}
	for _, row := range rows {
		addTokens(row)
	}
	for _, row := range unknownDateRows {
		addTokens(row)
	}
	totals.CostUSD = moneyUSD(breakdown.Total)
	totals.PricedTokens = breakdown.PricedTokens
	totals.UnpricedTokens = breakdown.UnpricedTokens + historyUsageTokenCount(unknownDateRows)
	return totals
}

func historyUsageTokenCount(rows []store.Usage) int64 {
	var total int64
	for _, row := range rows {
		total += row.Input + row.Output
	}
	return total
}

func historySessionCostModels(rows, unknownDateRows []store.Usage, table pricinglib.Table, warnings *[]string) []historySessionCostModel {
	models := make([]historySessionCostModel, 0, len(rows)+len(unknownDateRows))
	for _, row := range unknownDateRows {
		models = append(models, historySessionCostModel{
			Date: "unknown", Provider: row.Provider, Model: row.Model,
			historySessionCostTokens: historySessionCostTokenTotals(nil, []store.Usage{row}, pricinglib.CostBreakdown{}),
		})
	}
	for _, row := range rows {
		models = append(models, historySessionCostModel{
			Date: row.Date, Provider: row.Provider, Model: row.Model,
			historySessionCostTokens: historySessionCostTokenTotals([]store.Usage{row}, nil, table.Cost(row)),
		})
	}
	if len(models) > maxHistorySessionCostModels {
		*warnings = append(*warnings, fmt.Sprintf("model detail is capped at %d rows; session totals include all usage", maxHistorySessionCostModels))
		models = models[:maxHistorySessionCostModels]
	}
	return models
}

func writeHistorySessionCosts(cmd *cobra.Command, data historySessionCostData) {
	fmt.Fprintf(cmd.OutOrStdout(), "%-38s %-8s %-24s %-12s %-12s %s\n", "SESSION", "PROVIDER", "LAST", "TOKENS", "COST", "PREVIEW")
	for _, row := range data.Sessions {
		last := "-"
		if row.LastTimestamp != nil {
			last = *row.LastTimestamp
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%d\t$%.2f\t%s\n", row.SessionID, row.Provider, last, row.Tokens.TotalTokens, row.Tokens.CostUSD, safePrettyPreview(row.Preview))
	}
}
