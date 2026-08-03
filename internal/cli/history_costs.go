package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/history"
	historyindexer "github.com/janiorvalle/tokenomnom/internal/history/indexer"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/ingest"
	claudeingest "github.com/janiorvalle/tokenomnom/internal/ingest/claude"
	codexingest "github.com/janiorvalle/tokenomnom/internal/ingest/codex"
	pricinglib "github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

const maxHistorySessionCostModels = 32

const historyActiveSessionWarning = "session active since last index; refreshes on next index"

const historySessionCostCacheAlgorithmVersion = "history-cost-v1"

const historyVaultCacheIdentityPrefix = "vault:"

const historyFallbackSessionWarning = "the preferred exact transcript location was unavailable; cost uses a fallback indexed location and may omit newer usage; restore the source and rerun `tokenomnom history index`"

const historyChangedSinceIndexWarning = "session bytes changed since last index; rerun `tokenomnom history index` to refresh exact attribution"

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
	command.Flags().IntVar(&flags.limit, "limit", historystore.DefaultSessionCostPageSize, fmt.Sprintf("maximum page rows (1-%d, default %d)", historystore.MaxSessionCostPageSize, historystore.DefaultSessionCostPageSize))
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
			location, _ := historyPresentationTimezone(cmd)
			var data historySessionCostData
			var warnings []string
			cacheStats := &historySessionCostCacheStats{}
			query := historystore.SessionCostQuery{Catalog: flags.query(cmd)}
			if len(args) == 1 {
				query.SessionID = args[0]
			}
			if err := withHistoryStore(cmd, func(database *historystore.Store) error {
				page, err := database.ListSessionCostSources(query)
				if err != nil {
					return err
				}
				data = historySessionCostData{
					Sessions:   make([]historySessionCostRow, 0, len(page.Sessions)),
					Page:       page.Page,
					Coverage:   page.Coverage,
					Generation: page.Generation,
					Bounds: historySessionCostBounds{
						DefaultSessionsPerPage:            historystore.DefaultSessionCostPageSize,
						MaxSessionsPerPage:                historystore.MaxSessionCostPageSize,
						MaxTranscriptCandidatesPerSession: historystore.MaxSessionCostCandidates,
						MaxModelRowsPerSession:            maxHistorySessionCostModels,
						NapkinMath:                        fmt.Sprintf("1,200 sessions / %d max rows = 12 pages; default page size is %d; each page parses at most %d preferred transcript locations", historystore.MaxSessionCostPageSize, historystore.DefaultSessionCostPageSize, historystore.MaxSessionCostPageSize*historystore.MaxSessionCostCandidates),
					},
				}
				warnings = append([]string{}, page.Warnings...)
				cache := newHistorySessionCostCache(cmd, database, table, time.Time{}, time.Time{}, *codexDir, *claudeDir, cacheStats)
				rows, err := priceHistorySessionsParallel(page.Sessions, func(session historystore.SessionCostSession) (historySessionCostRow, error) {
					presentHistorySession(&session.CatalogSession, location)
					return priceHistorySessionMatchingCached(cmd, session, table, *codexDir, *claudeDir, allHistoryUsageEvents, cache)
				})
				if err != nil {
					return err
				}
				data.Sessions = rows
				data.Cache = cacheStats.snapshot()
				for _, row := range rows {
					for _, warning := range row.Warnings {
						warnings = append(warnings, fmt.Sprintf("session %s: %s", row.SessionID, warning))
					}
				}
				return nil
			}); err != nil {
				return err
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
	Sessions   []historySessionCostRow        `json:"sessions"`
	Page       historystore.PageMetadata      `json:"page"`
	Coverage   historystore.CatalogCoverage   `json:"coverage"`
	Generation int64                          `json:"index_generation"`
	Bounds     historySessionCostBounds       `json:"bounds"`
	Cache      historySessionCostCacheReceipt `json:"cache"`
}

type historySessionCostBounds struct {
	DefaultSessionsPerPage            int    `json:"default_sessions_per_page"`
	MaxSessionsPerPage                int    `json:"max_sessions_per_page"`
	MaxTranscriptCandidatesPerSession int    `json:"max_transcript_candidates_per_session"`
	MaxModelRowsPerSession            int    `json:"max_model_rows_per_session"`
	NapkinMath                        string `json:"napkin_math"`
}

type historySessionCostRow struct {
	historystore.CatalogSession
	Tokens               historySessionCostTokens  `json:"tokens"`
	Models               []historySessionCostModel `json:"models"`
	AttributionStatus    string                    `json:"attribution_status"`
	TokenSource          string                    `json:"token_source"`
	RawLocationKind      string                    `json:"raw_location_kind,omitempty"`
	MissingSourceSettled bool                      `json:"missing_source_settled,omitempty"`
	Warnings             []string                  `json:"warnings"`
	attributionTimestamp string
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
	cost                         pricinglib.Money
}

type historySessionCostCacheReceipt struct {
	Hits        int `json:"hits"`
	Misses      int `json:"misses"`
	Stores      int `json:"stores"`
	StoreErrors int `json:"store_errors"`
}

type historySessionCostCacheStats struct {
	mu      sync.Mutex
	receipt historySessionCostCacheReceipt
}

func (stats *historySessionCostCacheStats) hit() {
	if stats == nil {
		return
	}
	stats.mu.Lock()
	stats.receipt.Hits++
	stats.mu.Unlock()
}

func (stats *historySessionCostCacheStats) miss() {
	if stats == nil {
		return
	}
	stats.mu.Lock()
	stats.receipt.Misses++
	stats.mu.Unlock()
}

func (stats *historySessionCostCacheStats) store() {
	if stats == nil {
		return
	}
	stats.mu.Lock()
	stats.receipt.Stores++
	stats.mu.Unlock()
}

func (stats *historySessionCostCacheStats) storeError() {
	if stats == nil {
		return
	}
	stats.mu.Lock()
	stats.receipt.StoreErrors++
	stats.mu.Unlock()
}

func (stats *historySessionCostCacheStats) snapshot() historySessionCostCacheReceipt {
	if stats == nil {
		return historySessionCostCacheReceipt{}
	}
	stats.mu.Lock()
	defer stats.mu.Unlock()
	return stats.receipt
}

type historySessionCostCache struct {
	database           *historystore.Store
	pricingFingerprint string
	windowSince        string
	windowUntil        string
	stats              *historySessionCostCacheStats
	command            *cobra.Command
	codexDir           string
	claudeDir          string
	vaultStateOnce     sync.Once
	vaultDir           string
	vaultBroken        map[string]bool
	vaultStateErr      error
}

type historySessionCostCacheValue struct {
	Tokens               historySessionCostTokens       `json:"tokens"`
	Models               []historySessionCostCacheModel `json:"models"`
	AttributionStatus    string                         `json:"attribution_status"`
	TokenSource          string                         `json:"token_source"`
	RawLocationKind      string                         `json:"raw_location_kind,omitempty"`
	MissingSourceSettled bool                           `json:"missing_source_settled,omitempty"`
	FileIdentity         string                         `json:"file_identity,omitempty"`
	Warnings             []string                       `json:"warnings"`
	AttributionTimestamp string                         `json:"attribution_timestamp,omitempty"`
	CostNanodollars      int64                          `json:"cost_nanodollars"`
}

type historySessionCostCacheModel struct {
	Date            string                   `json:"date"`
	Provider        discover.Provider        `json:"provider"`
	Model           string                   `json:"model"`
	Tokens          historySessionCostTokens `json:"tokens"`
	CostNanodollars int64                    `json:"cost_nanodollars"`
}

func newHistorySessionCostCache(cmd *cobra.Command, database *historystore.Store, table pricinglib.Table, since, until time.Time, codexDir, claudeDir string, stats *historySessionCostCacheStats) *historySessionCostCache {
	return &historySessionCostCache{
		database: database, pricingFingerprint: historySessionCostCacheFingerprint(table),
		windowSince: historyCostCacheTime(since), windowUntil: historyCostCacheTime(until), stats: stats,
		command: cmd, codexDir: codexDir, claudeDir: claudeDir,
	}
}

func historySessionCostCacheFingerprint(table pricinglib.Table) string {
	return historySessionCostCacheAlgorithmVersion + ":" + table.Fingerprint()
}

func historyCostCacheTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func (cache *historySessionCostCache) key(session historystore.SessionCostSession, candidate historystore.RawCandidate) historystore.SessionCostCacheKey {
	return historystore.SessionCostCacheKey{
		SessionID: session.SessionID, LocationID: historySessionCostCandidateLocationID(candidate),
		ContentSHA256: candidate.ContentSHA256, ContentSize: candidate.Size,
		PricingFingerprint: cache.pricingFingerprint, CandidateContext: historySessionCostCandidateContext(session),
		WindowSince: cache.windowSince, WindowUntil: cache.windowUntil,
	}
}

func historySessionCostCandidateLocationID(candidate historystore.RawCandidate) string {
	if candidate.SourceHeadID != nil {
		return *candidate.SourceHeadID
	}
	if candidate.SnapshotID == nil {
		return ""
	}
	location := struct {
		SnapshotID   string `json:"snapshot_id"`
		SourcePath   string `json:"source_path"`
		Archive      string `json:"archive"`
		RelativePath string `json:"relative_path"`
		VaultVersion int    `json:"vault_version"`
	}{
		SnapshotID: *candidate.SnapshotID, SourcePath: candidate.SourcePath, Archive: candidate.Archive,
		RelativePath: candidate.RelativePath, VaultVersion: candidate.VaultVersion,
	}
	payload, _ := json.Marshal(location)
	digest := sha256.Sum256(payload)
	return "vault:" + *candidate.SnapshotID + ":" + hex.EncodeToString(digest[:])
}

type historySessionCostCandidateFingerprint struct {
	Kind          string `json:"kind"`
	LocationID    string `json:"location_id"`
	ContentSHA256 string `json:"content_sha256"`
	Size          int64  `json:"size"`
	ModTimeUnix   int64  `json:"mtime_unix"`
}

type historySessionCostCandidateContextValue struct {
	CandidateCount      int                                      `json:"candidate_count"`
	CandidatesTruncated bool                                     `json:"candidates_truncated"`
	Candidates          []historySessionCostCandidateFingerprint `json:"candidates"`
}

func historySessionCostCandidateContext(session historystore.SessionCostSession) string {
	candidates := make([]historySessionCostCandidateFingerprint, 0, len(session.Candidates))
	for _, candidate := range session.Candidates {
		candidates = append(candidates, historySessionCostCandidateFingerprint{
			Kind: candidate.Kind, LocationID: historySessionCostCandidateLocationID(candidate), ContentSHA256: candidate.ContentSHA256,
			Size: candidate.Size, ModTimeUnix: candidate.ModTimeUnix,
		})
	}
	payload, _ := json.Marshal(historySessionCostCandidateContextValue{
		CandidateCount: session.CandidateCount, CandidatesTruncated: session.CandidatesTruncated,
		Candidates: candidates,
	})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func (cache *historySessionCostCache) load(session historystore.SessionCostSession) (historySessionCostRow, bool) {
	if cache == nil || cache.database == nil || len(session.Candidates) == 0 {
		return historySessionCostRow{}, false
	}
	candidate := session.Candidates[0]
	if historyCandidateChangedSinceIndex(candidate) {
		cache.stats.miss()
		return historySessionCostRow{}, false
	}
	value, found, err := cache.loadCandidate(session, candidate)
	if err != nil {
		cache.stats.storeError()
		cache.stats.miss()
		return historySessionCostRow{}, false
	}
	if !found {
		cache.stats.miss()
		return historySessionCostRow{}, false
	}
	cache.stats.hit()
	return value, true
}

func (cache *historySessionCostCache) loadCandidate(session historystore.SessionCostSession, candidate historystore.RawCandidate) (historySessionCostRow, bool, error) {
	if cache == nil || cache.database == nil || historyCandidateChangedSinceIndex(candidate) {
		return historySessionCostRow{}, false, nil
	}
	payload, found, err := cache.database.GetSessionCostCache(cache.key(session, candidate))
	if err != nil || !found {
		return historySessionCostRow{}, found, err
	}
	var value historySessionCostCacheValue
	if err := json.Unmarshal(payload, &value); err != nil {
		return historySessionCostRow{}, false, err
	}
	if !cache.candidateCacheIdentityMatches(candidate, value.FileIdentity) {
		return historySessionCostRow{}, false, nil
	}
	if value.AttributionStatus == "" || value.TokenSource == "" {
		return historySessionCostRow{}, false, errors.New("history session cost cache payload is incomplete")
	}
	return value.row(session), true, nil
}

func (cache *historySessionCostCache) save(session historystore.SessionCostSession, candidate historystore.RawCandidate, row historySessionCostRow, candidateIdentity string) {
	if cache == nil || cache.database == nil || len(session.Candidates) == 0 || historyCandidateChangedSinceIndex(candidate) {
		return
	}
	if !cache.candidateCacheIdentityStable(candidate, candidateIdentity) {
		return
	}
	value := historySessionCostCacheValueFromRow(row)
	value.FileIdentity = candidateIdentity
	payload, err := json.Marshal(value)
	if err != nil {
		cache.stats.storeError()
		return
	}
	if err := cache.database.PutSessionCostCache(cache.key(session, candidate), payload); err != nil {
		cache.stats.storeError()
		return
	}
	cache.stats.store()
}

func historySessionCostCacheValueFromRow(row historySessionCostRow) historySessionCostCacheValue {
	value := historySessionCostCacheValue{
		Tokens: row.Tokens, AttributionStatus: row.AttributionStatus, TokenSource: row.TokenSource,
		RawLocationKind: row.RawLocationKind, MissingSourceSettled: row.MissingSourceSettled,
		Warnings: append([]string{}, row.Warnings...), AttributionTimestamp: row.attributionTimestamp,
		CostNanodollars: int64(row.Tokens.cost),
		Models:          make([]historySessionCostCacheModel, 0, len(row.Models)),
	}
	for _, model := range row.Models {
		value.Models = append(value.Models, historySessionCostCacheModel{
			Date: model.Date, Provider: model.Provider, Model: model.Model,
			Tokens: model.historySessionCostTokens, CostNanodollars: int64(model.cost),
		})
	}
	return value
}

func (cache *historySessionCostCache) candidateCacheIdentityMatches(candidate historystore.RawCandidate, cached string) bool {
	if candidate.Kind != "provider_live" && candidate.Kind != "provider_archive" {
		if candidate.Kind != "vault" {
			return true
		}
		identity := cache.candidateCacheIdentity(candidate)
		return identity != "" && identity == cached
	}
	identity, reliable := historyCandidateCacheIdentity(candidate)
	if !reliable {
		return historyCandidateMatchesIndexedBytes(candidate)
	}
	return identity != "" && identity == cached
}

func (cache *historySessionCostCache) candidateCacheIdentity(candidate historystore.RawCandidate) string {
	if candidate.Kind == "vault" {
		cache.vaultStateOnce.Do(func() {
			if cache.command == nil {
				cache.vaultStateErr = errors.New("history vault cache state requires a command")
				return
			}
			instance, database, err := openVault(cache.command, cache.codexDir, cache.claudeDir)
			if err != nil {
				cache.vaultStateErr = err
				return
			}
			defer database.Close()
			value, err := database.Meta("vault_broken_archives")
			if err != nil {
				cache.vaultStateErr = err
				return
			}
			cache.vaultDir = instance.Dir()
			cache.vaultBroken = map[string]bool{}
			if value != "" {
				var broken []string
				if err := json.Unmarshal([]byte(value), &broken); err != nil {
					cache.vaultStateErr = err
					return
				}
				for _, archive := range broken {
					cache.vaultBroken[archive] = true
				}
			}
		})
		if cache.vaultStateErr != nil || cache.vaultBroken[candidate.Archive] {
			return ""
		}
		archivePath := filepath.Join(cache.vaultDir, filepath.FromSlash(candidate.Archive))
		info, err := os.Stat(archivePath)
		if err != nil || !info.Mode().IsRegular() {
			return ""
		}
		// Vault hits validate the immutable archive with metadata only. A changed
		// bundle must take the cold exact-member path; re-reading unchanged
		// compressed bytes defeats the point of caching.
		return historyVaultCacheIdentity(info)
	}
	identity, _ := historyCandidateCacheIdentity(candidate)
	return identity
}

func (cache *historySessionCostCache) candidateCacheIdentityStable(candidate historystore.RawCandidate, before string) bool {
	if candidate.Kind == "provider_live" || candidate.Kind == "provider_archive" {
		_, reliable := historyCandidateCacheIdentity(candidate)
		if !reliable {
			return true
		}
	}
	return before != "" && cache.candidateCacheIdentity(candidate) == before
}

func historyVaultCacheIdentity(info os.FileInfo) string {
	if info == nil || !info.Mode().IsRegular() {
		return ""
	}
	identity := historyStableFileIdentity(info)
	return fmt.Sprintf("%s%s:size=%d:mtime=%d", historyVaultCacheIdentityPrefix, identity, info.Size(), info.ModTime().UnixNano())
}

func historyCandidateCacheIdentity(candidate historystore.RawCandidate) (string, bool) {
	if candidate.Kind != "provider_live" && candidate.Kind != "provider_archive" {
		return "", true
	}
	if runtime.GOOS == "windows" {
		return "", false
	}
	if !historyFilesystemHasReliableIdentity(candidate.SourcePath) {
		return "", false
	}
	info, err := os.Stat(candidate.SourcePath)
	if err != nil || info.Sys() == nil {
		return "", false
	}
	// Stat metadata includes the file-instance/change token on supported
	// providers, catching same-size replacements that preserve mtime without
	// rereading the transcript.
	identity := historyStableFileIdentity(info)
	if identity == "" {
		return "", false
	}
	return identity, true
}

func historyStableFileIdentity(info os.FileInfo) string {
	if info == nil || info.Sys() == nil {
		return ""
	}
	value := reflect.ValueOf(info.Sys())
	for value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return ""
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return ""
	}
	fieldNames := []string{
		"Dev", "Ino", "Ctim", "Ctimespec", "Birthtime", "Birthtimespec",
		"VolumeSerialNumber", "FileIndexHigh", "FileIndexLow", "CreationTime", "ChangeTime",
	}
	fields := make([]string, 0, len(fieldNames))
	for _, name := range fieldNames {
		field := value.FieldByName(name)
		if !field.IsValid() || !field.CanInterface() {
			continue
		}
		fields = append(fields, name+"="+fmt.Sprintf("%#v", field.Interface()))
	}
	if len(fields) == 0 {
		if runtime.GOOS == "windows" {
			// Windows vault hits are byte-verified below; this metadata only
			// preserves a persisted row until that check runs.
			return fmt.Sprintf("%T:%#v", info.Sys(), info.Sys())
		}
		return ""
	}
	return fmt.Sprintf("%T:%s", info.Sys(), strings.Join(fields, ";"))
}

func historyCandidateMatchesIndexedBytes(candidate historystore.RawCandidate) bool {
	if candidate.Kind != "provider_live" && candidate.Kind != "provider_archive" {
		return true
	}
	if candidate.ContentSHA256 == "" || historyCandidateChangedSinceIndex(candidate) {
		return false
	}
	source, err := os.Open(candidate.SourcePath)
	if err != nil {
		return false
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, source)
	closeErr := source.Close()
	return copyErr == nil && closeErr == nil && written == candidate.Size && hex.EncodeToString(hash.Sum(nil)) == candidate.ContentSHA256
}

func (value historySessionCostCacheValue) row(session historystore.SessionCostSession) historySessionCostRow {
	row := historySessionCostRow{
		CatalogSession: session.CatalogSession, Tokens: value.Tokens, AttributionStatus: value.AttributionStatus,
		TokenSource: value.TokenSource, RawLocationKind: value.RawLocationKind,
		MissingSourceSettled: session.MissingSourceSettled, Warnings: append([]string{}, value.Warnings...),
		attributionTimestamp: value.AttributionTimestamp,
	}
	row.Tokens.cost = pricinglib.Money(value.CostNanodollars)
	row.Models = make([]historySessionCostModel, 0, len(value.Models))
	for _, model := range value.Models {
		tokens := model.Tokens
		tokens.cost = pricinglib.Money(model.CostNanodollars)
		row.Models = append(row.Models, historySessionCostModel{
			Date: model.Date, Provider: model.Provider, Model: model.Model,
			historySessionCostTokens: tokens,
		})
	}
	return row
}

func priceHistorySession(cmd *cobra.Command, session historystore.SessionCostSession, table pricinglib.Table, codexDir, claudeDir string) (historySessionCostRow, error) {
	return priceHistorySessionMatching(cmd, session, table, codexDir, claudeDir, allHistoryUsageEvents)
}

func priceHistorySessionForWindow(cmd *cobra.Command, session historystore.SessionCostSession, table pricinglib.Table, codexDir, claudeDir string, since, until time.Time) (historySessionCostRow, error) {
	return priceHistorySessionMatching(cmd, session, table, codexDir, claudeDir, func(events []ingest.UsageEvent) ([]ingest.UsageEvent, []string) {
		return historyUsageEventsForWindow(events, since, until)
	})
}

func priceHistorySessionMatching(cmd *cobra.Command, session historystore.SessionCostSession, table pricinglib.Table, codexDir, claudeDir string, selectEvents func([]ingest.UsageEvent) ([]ingest.UsageEvent, []string)) (historySessionCostRow, error) {
	return priceHistorySessionMatchingCached(cmd, session, table, codexDir, claudeDir, selectEvents, nil)
}

func priceHistorySessionMatchingCached(cmd *cobra.Command, session historystore.SessionCostSession, table pricinglib.Table, codexDir, claudeDir string, selectEvents func([]ingest.UsageEvent) ([]ingest.UsageEvent, []string), cache *historySessionCostCache) (historySessionCostRow, error) {
	row := historySessionCostRow{
		CatalogSession:       session.CatalogSession,
		Models:               []historySessionCostModel{},
		TokenSource:          "indexed_exact_transcript",
		MissingSourceSettled: session.MissingSourceSettled,
		Warnings:             []string{},
	}
	if cached, ok := cache.load(session); ok {
		addHistoryCandidateTruncationWarning(&cached, session)
		return cached, nil
	}
	if session.CandidatesTruncated {
		row.Warnings = append(row.Warnings, fmt.Sprintf("only the preferred first %d of %d exact transcript locations were considered", len(session.Candidates), session.CandidateCount))
	}
	if len(session.Candidates) == 0 {
		if session.MissingSourceSettled {
			row.AttributionStatus = "settled_missing"
			row.TokenSource = "settled_missing"
			return row, nil
		}
		row.AttributionStatus = "unavailable"
		row.Warnings = append(row.Warnings, "no readable exact transcript bytes were available; restore the source or vault snapshot and rerun `tokenomnom history index`")
		return row, nil
	}

	var events []ingest.UsageEvent
	var selectedKind string
	var selectedCandidate historystore.RawCandidate
	var selectedIdentity string
	fallbackUsed := false
	for candidateIndex, candidate := range session.Candidates {
		// A growing or truncated provider file cannot satisfy the cache key or
		// exact-byte contract. Skip it before opening the potentially large file.
		// An mtime-only change still gets a cold exact-byte read below.
		if historyCandidateSizeChangedSinceIndex(candidate) {
			continue
		}
		if candidateIndex > 0 && cache != nil {
			cached, found, cacheErr := cache.loadCandidate(session, candidate)
			if cacheErr != nil {
				cache.stats.storeError()
			} else if found {
				cache.stats.hit()
				addHistoryFallbackWarning(&cached, session)
				addHistoryCandidateTruncationWarning(&cached, session)
				return cached, nil
			}
		}
		candidateIdentity := ""
		if cache != nil {
			candidateIdentity = cache.candidateCacheIdentity(candidate)
		}
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
		selectedCandidate = candidate
		selectedIdentity = candidateIdentity
		fallbackUsed = candidateIndex > 0
		break
	}
	if selectedKind == "" {
		row.AttributionStatus = "unavailable"
		warning := "indexed transcript bytes could not be read or parsed; restore the source or vault snapshot and rerun `tokenomnom history index`"
		if activeHistorySessionWarning(session.Candidates) != "" {
			warning = historyActiveSessionWarning
		} else if historyCandidatesHaveStatDrift(session.Candidates) {
			warning = historyChangedSinceIndexWarning
		}
		row.Warnings = append(row.Warnings, warning)
		return row, nil
	}
	events, selectionWarnings := selectEvents(events)
	row.Warnings = append(row.Warnings, selectionWarnings...)
	row.attributionTimestamp = firstHistoryUsageTimestamp(events)
	row.RawLocationKind = selectedKind
	if fallbackUsed {
		addHistoryFallbackWarning(&row, session)
	}
	usageRows, unknownDateRows, usageWarnings := aggregateHistoryUsage(events, session.Provider)
	row.Warnings = append(row.Warnings, usageWarnings...)
	if len(usageRows) == 0 && len(unknownDateRows) == 0 {
		row.AttributionStatus = "no_usage_events"
		if fallbackUsed {
			row.AttributionStatus = "incomplete"
		}
		row.Warnings = append(row.Warnings, "the exact transcript was read but contained no token-usage records; no cost was calculated")
		if cache != nil {
			cache.save(session, selectedCandidate, row, selectedIdentity)
		}
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
	if cache != nil {
		cache.save(session, selectedCandidate, row, selectedIdentity)
	}
	return row, nil
}

func addHistoryCandidateTruncationWarning(row *historySessionCostRow, session historystore.SessionCostSession) {
	if row == nil || !session.CandidatesTruncated {
		return
	}
	warning := fmt.Sprintf("only the preferred first %d of %d exact transcript locations were considered", len(session.Candidates), session.CandidateCount)
	for _, existing := range row.Warnings {
		if existing == warning {
			return
		}
	}
	row.Warnings = append(row.Warnings, warning)
}

func addHistoryFallbackWarning(row *historySessionCostRow, session historystore.SessionCostSession) {
	if row == nil {
		return
	}
	filtered := row.Warnings[:0]
	for _, warning := range row.Warnings {
		if warning != historyActiveSessionWarning && warning != historyFallbackSessionWarning && warning != historyChangedSinceIndexWarning {
			filtered = append(filtered, warning)
		}
	}
	row.Warnings = filtered
	preferredChanged := len(session.Candidates) > 0 && historyCandidateChangedSinceIndex(session.Candidates[0])
	if preferredChanged && historyCandidateIsGrowing(session.Candidates[0]) {
		row.Warnings = append(row.Warnings, historyActiveSessionWarning)
		return
	}
	if preferredChanged && historyCandidateHasStatDrift(session.Candidates[0]) {
		row.Warnings = append(row.Warnings, historyChangedSinceIndexWarning)
		return
	}
	row.Warnings = append(row.Warnings, historyFallbackSessionWarning)
}

func priceHistorySessionsParallel(sessions []historystore.SessionCostSession, price func(historystore.SessionCostSession) (historySessionCostRow, error)) ([]historySessionCostRow, error) {
	rows := make([]historySessionCostRow, len(sessions))
	if len(sessions) == 0 {
		return rows, nil
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(sessions) {
		workers = len(sessions)
	}
	jobs := make(chan int)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for index := range jobs {
				row, err := price(sessions[index])
				rows[index] = historySessionCostRow{CatalogSession: sessions[index].CatalogSession}
				if err == nil {
					rows[index] = row
				}
				if err != nil {
					rows[index].Warnings = []string{err.Error()}
				}
			}
		}()
	}
	for index := range sessions {
		jobs <- index
	}
	close(jobs)
	wait.Wait()
	for _, row := range rows {
		if len(row.Warnings) == 1 && row.AttributionStatus == "" {
			return nil, errors.New(row.Warnings[0])
		}
	}
	return rows, nil
}

func historySessionCostRowIsActive(row historySessionCostRow) bool {
	for _, warning := range row.Warnings {
		if warning == historyActiveSessionWarning {
			return true
		}
	}
	return false
}

func activeHistorySessionWarning(candidates []historystore.RawCandidate) string {
	for _, candidate := range candidates {
		if historyCandidateIsGrowing(candidate) {
			return historyActiveSessionWarning
		}
	}
	return ""
}

func historyCandidateHasStatDrift(candidate historystore.RawCandidate) bool {
	if candidate.Kind != "provider_live" && candidate.Kind != "provider_archive" {
		return false
	}
	info, err := os.Stat(candidate.SourcePath)
	if err != nil {
		return false
	}
	if info.Size() != candidate.Size {
		return true
	}
	return candidate.ModTimeUnix != 0 && info.ModTime().UnixNano() != candidate.ModTimeUnix
}

func historyCandidatesHaveStatDrift(candidates []historystore.RawCandidate) bool {
	for _, candidate := range candidates {
		if historyCandidateHasStatDrift(candidate) {
			return true
		}
	}
	return false
}

func historyCandidateIsGrowing(candidate historystore.RawCandidate) bool {
	if candidate.Kind != "provider_live" && candidate.Kind != "provider_archive" {
		return false
	}
	info, err := os.Stat(candidate.SourcePath)
	if err != nil || candidate.Size < 0 || info.Size() <= candidate.Size || candidate.PrefixFingerprint == "" {
		return false
	}
	prefix, err := historyindexer.PrefixFingerprint(candidate.SourcePath, candidate.Size)
	return err == nil && prefix == candidate.PrefixFingerprint
}

func historyCandidateChangedSinceIndex(candidate historystore.RawCandidate) bool {
	if candidate.Kind != "provider_live" && candidate.Kind != "provider_archive" {
		return false
	}
	info, err := os.Stat(candidate.SourcePath)
	if err != nil || info.Size() != candidate.Size {
		return true
	}
	return candidate.ModTimeUnix != 0 && info.ModTime().UnixNano() != candidate.ModTimeUnix
}

func historyCandidateSizeChangedSinceIndex(candidate historystore.RawCandidate) bool {
	if candidate.Kind != "provider_live" && candidate.Kind != "provider_archive" {
		return false
	}
	info, err := os.Stat(candidate.SourcePath)
	return err != nil || info.Size() != candidate.Size
}

func allHistoryUsageEvents(events []ingest.UsageEvent) ([]ingest.UsageEvent, []string) {
	return events, nil
}

func historyUsageEventsForWindow(events []ingest.UsageEvent, since, until time.Time) ([]ingest.UsageEvent, []string) {
	selected := make([]ingest.UsageEvent, 0, len(events))
	missingTimestampRecords := 0
	var missingTimestampTokens int64
	for _, event := range events {
		if event.Timestamp.IsZero() {
			missingTimestampRecords++
			missingTimestampTokens += event.Input + event.Output
			continue
		}
		if !event.Timestamp.Before(since) && !event.Timestamp.After(until) {
			selected = append(selected, event)
		}
	}
	if missingTimestampRecords == 0 {
		return selected, nil
	}
	recordLabel := "records"
	if missingTimestampRecords == 1 {
		recordLabel = "record"
	}
	return selected, []string{fmt.Sprintf("%d token-usage %s had no timestamp; %d tokens are excluded from day-scoped totals", missingTimestampRecords, recordLabel, missingTimestampTokens)}
}

func firstHistoryUsageTimestamp(events []ingest.UsageEvent) string {
	var first time.Time
	for _, event := range events {
		if event.Timestamp.IsZero() || !first.IsZero() && !event.Timestamp.Before(first) {
			continue
		}
		first = event.Timestamp
	}
	if first.IsZero() {
		return ""
	}
	return first.Format(time.RFC3339Nano)
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
	totals.cost = breakdown.Total
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
		fmt.Fprintf(cmd.OutOrStdout(), "%-38s %-8s %-24s %-12d %-12s %s\n", row.SessionID, row.Provider, last, row.Tokens.TotalTokens, fmt.Sprintf("$%.2f", row.Tokens.CostUSD), safePrettyPreview(row.Preview))
	}
}
