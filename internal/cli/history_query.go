package cli

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
)

const maxHistoryRawJSONBytes int64 = 64 << 20

type historyQueryFlags struct {
	provider       string
	since          string
	until          string
	cwd            string
	repo           string
	branch         string
	source         string
	limit          int
	cursor         string
	threadKind     string
	rootOnly       bool
	role           string
	promptKind     string
	excludeControl bool
}

func (flags *historyQueryFlags) addRole(command *cobra.Command) {
	command.Flags().StringVar(&flags.role, "role", "user", "filter by message role (user, assistant, or any)")
}

func (flags *historyQueryFlags) add(command *cobra.Command, defaultLimit int) {
	command.Flags().StringVar(&flags.provider, "provider", "", "filter by provider (codex or claude)")
	command.Flags().StringVar(&flags.since, "since", "", "include prompts on or after YYYY-MM-DD")
	command.Flags().StringVar(&flags.until, "until", "", "include prompts on or before YYYY-MM-DD")
	command.Flags().StringVar(&flags.cwd, "cwd", "", "filter by exact working directory")
	command.Flags().StringVar(&flags.repo, "repo", "", "filter by known repository name")
	command.Flags().StringVar(&flags.branch, "branch", "", "filter by known branch")
	command.Flags().StringVar(&flags.source, "source", "any", "filter by availability source")
	command.Flags().IntVar(&flags.limit, "limit", defaultLimit, "maximum page rows (1-500)")
	command.Flags().StringVar(&flags.cursor, "cursor", "", "continue a previous page")
	command.Flags().StringVar(&flags.threadKind, "thread-kind", "all", "filter by thread kind (root, subagent, unknown, or all)")
	command.Flags().BoolVar(&flags.rootOnly, "root-only", false, "include only directly evidenced root sessions")
	command.Flags().StringVar(&flags.promptKind, "prompt-kind", "", "filter by comma-separated prompt kinds")
	command.Flags().BoolVar(&flags.excludeControl, "exclude-control", false, "exclude control prompts")
}

func (flags historyQueryFlags) validate(command *cobra.Command) error {
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
	if flags.role != "" && flags.role != "user" && flags.role != "assistant" && flags.role != "any" {
		return fmt.Errorf("invalid --role %q (expected user, assistant, or any)", flags.role)
	}
	if _, err := flags.promptKinds(); err != nil {
		return err
	}
	if command.Flags().Changed("limit") && (flags.limit < 1 || flags.limit > 500) {
		return errors.New("--limit must be between 1 and 500")
	}
	return nil
}

func (flags historyQueryFlags) query(command *cobra.Command) historystore.PromptQuery {
	limit := flags.limit
	if flags.cursor != "" && !command.Flags().Changed("limit") {
		limit = 0
	}
	query := historystore.PromptQuery{
		Provider: history.Provider(flags.provider), CWD: flags.cwd, Repo: flags.repo, Branch: flags.branch,
		Source: historystore.CatalogSource(flags.source), ThreadKind: flags.effectiveThreadKind(), Role: flags.role,
		AssistantConsent: appconfig.FromContext(command.Context()).Config.History.IndexAssistant, Limit: limit, Cursor: flags.cursor,
		ExcludeControl: flags.excludeControl,
	}
	query.PromptKinds, _ = flags.promptKinds()
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

func (flags historyQueryFlags) jsonFilters() jsonFilters {
	return jsonFilters{Provider: optionalString(flags.provider), Since: optionalString(flags.since), Until: optionalString(flags.until), ThreadKind: optionalString(flags.effectiveThreadKind()), Role: optionalString(flags.role), PromptKind: optionalString(flags.promptKind), ExcludeControl: flags.excludeControl}
}

func (flags historyQueryFlags) promptKinds() ([]history.PromptKind, error) {
	if strings.TrimSpace(flags.promptKind) == "" {
		return nil, nil
	}
	seen := map[history.PromptKind]bool{}
	result := []history.PromptKind{}
	for _, raw := range strings.Split(flags.promptKind, ",") {
		kind := history.PromptKind(strings.TrimSpace(raw))
		switch kind {
		case history.PromptKindHuman, history.PromptKindDelegation, history.PromptKindAgentMessage, history.PromptKindCommand, history.PromptKindControl, history.PromptKindUnknown:
		default:
			return nil, fmt.Errorf("invalid --prompt-kind %q", raw)
		}
		if !seen[kind] {
			seen[kind] = true
			result = append(result, kind)
		}
	}
	return result, nil
}

func (flags historyQueryFlags) effectiveThreadKind() string {
	if flags.rootOnly {
		return "root"
	}
	return flags.threadKind
}

func newHistorySearchCommand() *cobra.Command {
	var flags historyQueryFlags
	var includeText, ftsQuery, allOccurrences bool
	command := &cobra.Command{
		Use:   "search <query>",
		Short: "Search indexed prompts",
		Args:  cobra.ExactArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("search query must not be empty")
			}
			return flags.validate(cmd)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			query := flags.query(cmd)
			query.IncludeText, query.AllOccurrences = includeText, allOccurrences
			var page historystore.SearchPage
			if err := withHistoryStore(cmd, func(database *historystore.Store) error {
				var err error
				page, err = database.Search(historystore.SearchQuery{PromptQuery: query, Query: args[0], FTSQuery: ftsQuery})
				return err
			}); err != nil {
				return err
			}
			location, _ := historyPresentationTimezone(cmd)
			presentHistorySearchPage(&page, location)
			if currentFormat(cmd) == "json" {
				return writeHistoryJSONEnvelope(cmd, "history search", flags.jsonFilters(), page.Warnings, page)
			}
			for _, hit := range page.Hits {
				timestamp := "-"
				if hit.Timestamp != nil {
					timestamp = *hit.Timestamp
				}
				rank := "-"
				if hit.Rank != nil {
					rank = fmt.Sprintf("%.8g", *hit.Rank)
				}
				if flags.role == "user" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", hit.PromptID, hit.Provider, timestamp, rank, safePrettyPreview(hit.Snippet))
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\t%s\n", hit.PromptID, hit.Provider, hit.Role, timestamp, rank, safePrettyPreview(hit.Snippet))
				}
				if includeText && hit.Text != nil {
					fmt.Fprintln(cmd.OutOrStdout(), safePrettyText(*hit.Text))
				}
			}
			writeHistoryWarnings(cmd, page.Warnings)
			writeHistoryContinuation(cmd, page.Page)
			return nil
		},
	}
	flags.add(command, 50)
	flags.addRole(command)
	command.Flags().BoolVar(&includeText, "include-text", false, "include complete clean prompt text")
	command.Flags().BoolVar(&ftsQuery, "fts-query", false, "interpret query as raw SQLite FTS5 syntax")
	command.Flags().BoolVar(&allOccurrences, "all-occurrences", false, "include bounded source and snapshot occurrence provenance")
	return command
}

func newHistoryPromptsCommand() *cobra.Command {
	var flags historyQueryFlags
	var includeText, allOccurrences bool
	command := &cobra.Command{
		Use:     "prompts",
		Short:   "List indexed prompts",
		Args:    cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error { return flags.validate(cmd) },
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := flags.query(cmd)
			query.IncludeText, query.AllOccurrences = includeText, allOccurrences
			var page historystore.PromptsPage
			if err := withHistoryStore(cmd, func(database *historystore.Store) error {
				var err error
				page, err = database.ListPrompts(query)
				return err
			}); err != nil {
				return err
			}
			location, _ := historyPresentationTimezone(cmd)
			presentHistoryPromptPage(&page, location)
			if currentFormat(cmd) == "json" {
				return writeHistoryJSONEnvelope(cmd, "history prompts", flags.jsonFilters(), page.Warnings, page)
			}
			for _, prompt := range page.Prompts {
				timestamp := "-"
				if prompt.Timestamp != nil {
					timestamp = *prompt.Timestamp
				}
				if flags.role == "user" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", prompt.PromptID, prompt.Provider, timestamp, safePrettyPreview(prompt.Snippet))
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", prompt.PromptID, prompt.Provider, prompt.Role, timestamp, safePrettyPreview(prompt.Snippet))
				}
				if includeText && prompt.Text != nil {
					fmt.Fprintln(cmd.OutOrStdout(), safePrettyText(*prompt.Text))
				}
			}
			writeHistoryWarnings(cmd, page.Warnings)
			writeHistoryContinuation(cmd, page.Page)
			return nil
		},
	}
	flags.add(command, 100)
	flags.addRole(command)
	command.Flags().BoolVar(&includeText, "include-text", false, "include complete clean prompt text")
	command.Flags().BoolVar(&allOccurrences, "all-occurrences", false, "include bounded source and snapshot occurrence provenance")
	return command
}

func newHistorySampleCommand() *cobra.Command {
	var flags historyQueryFlags
	var unit, strategy, groupBy, seed string
	var count int
	var includeText, allOccurrences, onePerSession bool
	var minLength int
	command := &cobra.Command{
		Use:   "sample",
		Short: "Sample indexed logical prompts or sessions",
		Args:  cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := flags.validate(cmd); err != nil {
				return err
			}
			if cmd.Flags().Changed("limit") || flags.cursor != "" {
				return errors.New("history sample does not support --limit or --cursor; use --count")
			}
			if unit != "prompt" && unit != "session" {
				return fmt.Errorf("invalid --unit %q (expected prompt or session)", unit)
			}
			if unit == "session" && (minLength != 0 || onePerSession || flags.promptKind != "" || flags.excludeControl || allOccurrences) {
				return errors.New("history session sampling does not support --min-length, --one-per-session, --prompt-kind, --exclude-control, or --all-occurrences")
			}
			if strategy != "" && strategy != "random" && strategy != "stratified" {
				return fmt.Errorf("invalid --strategy %q (expected random or stratified)", strategy)
			}
			if count < 1 || count > 100 {
				return errors.New("--count must be between 1 and 100")
			}
			if minLength < 0 {
				return errors.New("--min-length must be zero or greater")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			groups := []string{}
			if groupBy != "" {
				for _, group := range strings.Split(groupBy, ",") {
					if group = strings.TrimSpace(group); group != "" {
						groups = append(groups, group)
					}
				}
			}
			query := flags.query(cmd)
			query.IncludeText, query.AllOccurrences = includeText, allOccurrences
			var result historystore.SampleResult
			if err := withHistoryStore(cmd, func(database *historystore.Store) error {
				var err error
				result, err = database.Sample(historystore.SampleQuery{PromptQuery: query, Unit: unit, Strategy: strategy, GroupBy: groups, Count: count, Seed: seed, MinLength: minLength, OnePerSession: onePerSession})
				return err
			}); err != nil {
				return err
			}
			location, _ := historyPresentationTimezone(cmd)
			presentHistorySample(&result, location)
			if currentFormat(cmd) == "json" {
				return writeHistoryJSONEnvelope(cmd, "history sample", flags.jsonFilters(), result.Warnings, result)
			}
			for _, item := range result.Items {
				groupParts := make([]string, 0, len(result.GroupBy))
				for _, group := range result.GroupBy {
					if value, ok := item.Groups[group]; ok {
						groupParts = append(groupParts, group+"="+safePrettyPreview(value))
					}
				}
				groupText := strings.Join(groupParts, ",")
				if item.Prompt != nil {
					timestamp := "-"
					if item.Prompt.Timestamp != nil {
						timestamp = *item.Prompt.Timestamp
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", item.Prompt.PromptID, item.Prompt.Provider, timestamp, groupText, safePrettyPreview(item.Prompt.Snippet))
					if includeText && item.Prompt.Text != nil {
						fmt.Fprintln(cmd.OutOrStdout(), safePrettyText(*item.Prompt.Text))
					}
					continue
				}
				if item.Session != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", item.Session.SessionID, item.Session.Provider, groupText, safePrettyPreview(item.Session.Preview))
					if includeText && item.Text != nil {
						fmt.Fprintln(cmd.OutOrStdout(), safePrettyText(*item.Text))
					}
				}
			}
			writeHistoryWarnings(cmd, result.Warnings)
			return nil
		},
	}
	flags.add(command, 100)
	_ = command.Flags().MarkHidden("limit")
	_ = command.Flags().MarkHidden("cursor")
	command.Flags().StringVar(&unit, "unit", "prompt", "sample logical prompts or sessions")
	command.Flags().StringVar(&strategy, "strategy", "", "sampling strategy (random or stratified)")
	command.Flags().StringVar(&groupBy, "group-by", "", "stratify by month, cwd, repo, and/or thread-kind")
	command.Flags().IntVar(&count, "count", 25, "maximum sampled units (1-100)")
	command.Flags().StringVar(&seed, "seed", "", "deterministic sample seed")
	command.Flags().BoolVar(&includeText, "include-text", false, "include complete clean prompt text")
	command.Flags().BoolVar(&allOccurrences, "all-occurrences", false, "include bounded source and snapshot occurrence provenance")
	command.Flags().IntVar(&minLength, "min-length", 0, "minimum cleaned prompt characters")
	command.Flags().BoolVar(&onePerSession, "one-per-session", false, "sample at most one prompt per session")
	return command
}

func newHistoryStatsCommand() *cobra.Command {
	var flags historyQueryFlags
	var groupBy string
	var top int
	command := &cobra.Command{
		Use:   "stats",
		Short: "Summarize the indexed prompt corpus",
		Args:  cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := flags.validate(cmd); err != nil {
				return err
			}
			if cmd.Flags().Changed("limit") || flags.cursor != "" {
				return errors.New("history stats does not support --limit or --cursor")
			}
			if top < 1 || top > 100 {
				return errors.New("--top must be between 1 and 100")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := flags.query(cmd)
			var value historystore.Statistics
			groups := []string{}
			seenGroups := map[string]bool{}
			if groupBy != "" {
				for _, group := range strings.Split(groupBy, ",") {
					group = strings.TrimSpace(group)
					if group != "" && !seenGroups[group] {
						groups = append(groups, group)
						seenGroups[group] = true
					}
				}
			}
			if err := withHistoryStore(cmd, func(database *historystore.Store) error {
				var err error
				value, err = database.Statistics(historystore.StatisticsQuery{PromptQuery: query, GroupBy: groups, Top: top})
				return err
			}); err != nil {
				return err
			}
			location, _ := historyPresentationTimezone(cmd)
			presentHistoryStatistics(&value, location)
			if currentFormat(cmd) == "json" {
				return writeHistoryJSONEnvelope(cmd, "history stats", flags.jsonFilters(), value.Warnings, value)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Scope: searchable prompt corpus\nSessions: %d\nPrompts: %d\nOccurrences: %d\nActive days: %d\nPrompt bytes: %d (median %.1f)\nIndex bytes: %d\n",
				value.LogicalSessions, value.LogicalPrompts, value.PromptOccurrences, value.ActiveDays,
				value.PromptLengthTotalBytes, value.PromptLengthMedianBytes, value.IndexSizeBytes)
			fmt.Fprintf(cmd.OutOrStdout(), "Role prompts: user=%d assistant=%d\n", value.RoleCounts.User.LogicalPrompts, value.RoleCounts.Assistant.LogicalPrompts)
			if slices.Contains(groups, "weekday") || slices.Contains(groups, "hour") {
				fmt.Fprintln(cmd.OutOrStdout(), "Time grouping: UTC")
			}
			for _, group := range value.Groups {
				parts := make([]string, 0, len(groups))
				for _, dimension := range groups {
					parts = append(parts, dimension+"="+safePrettyPreview(group.Values[dimension]))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\tsessions=%d\tprompts=%d\toccurrences=%d\n", strings.Join(parts, ","), group.LogicalSessions, group.LogicalPrompts, group.PromptOccurrences)
			}
			if value.Other != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "other\tsessions=%d\tprompts=%d\toccurrences=%d\n", value.Other.LogicalSessions, value.Other.LogicalPrompts, value.Other.PromptOccurrences)
			}
			writeHistoryWarnings(cmd, value.Warnings)
			return nil
		},
	}
	flags.add(command, 100)
	_ = command.Flags().MarkHidden("limit")
	_ = command.Flags().MarkHidden("cursor")
	command.Flags().StringVar(&groupBy, "group-by", "", "group by provider, repo, cwd, thread-kind, weekday, hour, and/or role")
	command.Flags().IntVar(&top, "top", 20, "maximum groups to return (1-100)")
	return command
}

func newHistoryShowCommand(codexDir, claudeDir *string) *cobra.Command {
	var prompts, raw bool
	var snapshot, cursor string
	var limit int
	command := &cobra.Command{
		Use:   "show <prompt-id|session-id>",
		Short: "Retrieve one indexed prompt or session",
		Args:  cobra.ExactArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if prompts && raw {
				return errors.New("--prompts and --raw are mutually exclusive")
			}
			if snapshot != "" && !raw {
				return errors.New("--snapshot requires --raw")
			}
			if !prompts && (cursor != "" || cmd.Flags().Changed("limit")) {
				return errors.New("--limit and --cursor require --prompts")
			}
			if strings.HasPrefix(args[0], "prm_") && (prompts || raw || snapshot != "" || cursor != "" || cmd.Flags().Changed("limit")) {
				return errors.New("prompt IDs do not accept session retrieval flags")
			}
			if cmd.Flags().Changed("limit") && (limit < 1 || limit > 500) {
				return errors.New("--limit must be between 1 and 500")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if strings.HasPrefix(id, "prm_") {
				var value historystore.PromptResult
				if err := withHistoryStore(cmd, func(database *historystore.Store) error {
					var err error
					value, err = database.GetPrompt(id)
					return err
				}); err != nil {
					return err
				}
				location, _ := historyPresentationTimezone(cmd)
				presentHistoryPrompt(&value, location)
				if currentFormat(cmd) == "json" {
					return writeHistoryJSONEnvelope(cmd, "history show", jsonFilters{}, nil, value)
				}
				if value.Text != nil {
					fmt.Fprintln(cmd.OutOrStdout(), safePrettyText(*value.Text))
				}
				return nil
			}
			if !strings.HasPrefix(id, "ses_") {
				return fmt.Errorf("%q is not a history prompt or session ID", id)
			}
			if raw {
				return showHistoryRaw(cmd, id, snapshot, *codexDir, *claudeDir)
			}
			if prompts {
				requestedLimit := limit
				if cursor != "" && !cmd.Flags().Changed("limit") {
					requestedLimit = 0
				}
				var page historystore.PromptsPage
				if err := withHistoryStore(cmd, func(database *historystore.Store) error {
					var err error
					page, err = database.SessionPrompts(id, historystore.PromptQuery{Limit: requestedLimit, Cursor: cursor})
					return err
				}); err != nil {
					return err
				}
				location, _ := historyPresentationTimezone(cmd)
				presentHistoryPromptPage(&page, location)
				if currentFormat(cmd) == "json" {
					return writeHistoryJSONEnvelope(cmd, "history show", jsonFilters{}, page.Warnings, page)
				}
				for _, value := range page.Prompts {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", value.PromptID, safePrettyPreview(*value.Text))
				}
				writeHistoryContinuation(cmd, page.Page)
				return nil
			}
			var value historystore.CatalogSession
			if err := withHistoryStore(cmd, func(database *historystore.Store) error {
				var err error
				value, err = database.GetSession(id)
				return err
			}); err != nil {
				return err
			}
			location, _ := historyPresentationTimezone(cmd)
			presentHistorySession(&value, location)
			if currentFormat(cmd) == "json" {
				return writeHistoryJSONEnvelope(cmd, "history show", jsonFilters{}, nil, value)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tprompts=%d\toccurrences=%d\t%s\n", value.SessionID, value.Provider, value.LogicalPromptCount, value.OccurrenceCount, safePrettyPreview(value.Preview))
			return nil
		},
	}
	command.Flags().BoolVar(&prompts, "prompts", false, "list clean prompts for a session")
	command.Flags().BoolVar(&raw, "raw", false, "retrieve exact indexed transcript bytes")
	command.Flags().StringVar(&snapshot, "snapshot", "", "retrieve one preserved snapshot ID")
	command.Flags().IntVar(&limit, "limit", 100, "maximum prompt page rows (1-500)")
	command.Flags().StringVar(&cursor, "cursor", "", "continue a session prompt page")
	return command
}

type historyRawData struct {
	SessionID     string                    `json:"session_id"`
	Location      historystore.RawCandidate `json:"location"`
	Encoding      string                    `json:"encoding"`
	Content       *string                   `json:"content"`
	ContentBase64 string                    `json:"content_base64"`
}

func showHistoryRaw(cmd *cobra.Command, sessionID, snapshotID, codexDir, claudeDir string) error {
	var candidates []historystore.RawCandidate
	if err := withHistoryStore(cmd, func(database *historystore.Store) error {
		var err error
		candidates, err = database.RawCandidates(sessionID, snapshotID)
		return err
	}); err != nil {
		return err
	}
	warnings := []string{}
	var selected historystore.RawCandidate
	var staged *stagedHistoryRaw
	var lastErr error
	jsonOutput := currentFormat(cmd) == "json"
	for _, candidate := range candidates {
		if jsonOutput && candidate.Size > maxHistoryRawJSONBytes {
			lastErr = fmt.Errorf("indexed %s raw transcript is %d bytes; raw JSON output is limited to %d bytes", candidate.Kind, candidate.Size, maxHistoryRawJSONBytes)
			warnings = append(warnings, lastErr.Error())
			continue
		}
		var err error
		staged, err = readHistoryRawCandidate(cmd, candidate, codexDir, claudeDir)
		if err == nil {
			selected = candidate
			lastErr = nil
			break
		}
		lastErr = err
		warnings = append(warnings, err.Error())
	}
	if lastErr != nil {
		return fmt.Errorf("no indexed exact raw location could be read: %w", lastErr)
	}
	defer staged.cleanup()
	if !jsonOutput {
		for _, warning := range warnings {
			fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: "+warning)
		}
		_, err := io.Copy(cmd.OutOrStdout(), staged.file)
		return err
	}
	if selected.Size > maxHistoryRawJSONBytes {
		return fmt.Errorf("raw JSON output is limited to %d bytes; omit --format json to stream exact bytes", maxHistoryRawJSONBytes)
	}
	content, err := io.ReadAll(io.LimitReader(staged.file, selected.Size+1))
	if err != nil {
		return fmt.Errorf("read validated raw history bytes: %w", err)
	}
	if int64(len(content)) != selected.Size {
		return errors.New("validated raw history staging size changed")
	}
	data := historyRawData{SessionID: sessionID, Location: selected, ContentBase64: base64.StdEncoding.EncodeToString(content)}
	if utf8.Valid(content) {
		value := string(content)
		data.Encoding, data.Content = "utf-8", &value
	} else {
		data.Encoding = "base64"
	}
	return writeHistoryJSONEnvelope(cmd, "history show", jsonFilters{}, warnings, data)
}

type stagedHistoryRaw struct {
	file *os.File
	path string
}

func (staged *stagedHistoryRaw) cleanup() {
	if staged == nil {
		return
	}
	_ = staged.file.Close()
	_ = os.Remove(staged.path)
}

func readHistoryRawCandidate(cmd *cobra.Command, candidate historystore.RawCandidate, codexDir, claudeDir string) (*stagedHistoryRaw, error) {
	staged, err := newStagedHistoryRaw()
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*stagedHistoryRaw, error) {
		staged.cleanup()
		return nil, err
	}
	if candidate.Kind == "provider_live" || candidate.Kind == "provider_archive" {
		source, err := os.Open(candidate.SourcePath)
		if err != nil {
			return fail(fmt.Errorf("indexed %s source %q is unavailable: %w", candidate.Kind, candidate.SourcePath, err))
		}
		defer source.Close()
		hash := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(staged.file, hash), source, candidate.Size+1)
		if copyErr != nil && !errors.Is(copyErr, io.EOF) {
			return fail(fmt.Errorf("read indexed %s source %q: %w", candidate.Kind, candidate.SourcePath, copyErr))
		}
		if written != candidate.Size || hex.EncodeToString(hash.Sum(nil)) != candidate.ContentSHA256 {
			return fail(fmt.Errorf("indexed %s source %q changed since indexing; refusing stale raw bytes", candidate.Kind, candidate.SourcePath))
		}
		if err := staged.rewind(); err != nil {
			return fail(err)
		}
		return staged, nil
	}
	if candidate.Kind != "vault" {
		return fail(fmt.Errorf("unsupported indexed raw location kind %q", candidate.Kind))
	}
	instance, usageDatabase, err := openVault(cmd, codexDir, claudeDir)
	if err != nil {
		return fail(err)
	}
	defer usageDatabase.Close()
	manifest, err := instance.Cat(candidate.SourcePath, candidate.VaultVersion, staged.file)
	if err != nil {
		return fail(fmt.Errorf("indexed vault snapshot %q is unavailable or broken: %w", candidate.Archive, err))
	}
	if manifest.ContentSHA256 != candidate.ContentSHA256 || manifest.Size != candidate.Size {
		return fail(errors.New("vault bytes do not match the indexed snapshot version"))
	}
	digest, size, err := staged.digest()
	if err != nil {
		return fail(err)
	}
	if size != candidate.Size || digest != candidate.ContentSHA256 {
		return fail(errors.New("vault bytes do not match the indexed snapshot version"))
	}
	if err := staged.rewind(); err != nil {
		return fail(err)
	}
	return staged, nil
}

func newStagedHistoryRaw() (*stagedHistoryRaw, error) {
	file, err := os.CreateTemp("", ".tokenomnom-history-raw-*")
	if err != nil {
		return nil, fmt.Errorf("create raw history staging file: %w", err)
	}
	return &stagedHistoryRaw{file: file, path: file.Name()}, nil
}

func (staged *stagedHistoryRaw) rewind() error {
	if _, err := staged.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind raw history staging file: %w", err)
	}
	return nil
}

func (staged *stagedHistoryRaw) digest() (string, int64, error) {
	if err := staged.rewind(); err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, staged.file)
	if err != nil {
		return "", 0, fmt.Errorf("verify raw history staging file: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func withHistoryStore(cmd *cobra.Command, run func(*historystore.Store) error) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path, err := historyDatabasePath(home)
	if err != nil {
		return err
	}
	info, err := historystore.Inspect(path)
	if err != nil {
		return err
	}
	if !info.Exists {
		return errors.New("history index does not exist; run tokenomnom history index first")
	}
	database, err := historystore.OpenReadOnly(path)
	if err != nil {
		return err
	}
	defer database.Close()
	return run(database)
}

func writeHistoryWarnings(cmd *cobra.Command, warnings []string) {
	for _, warning := range warnings {
		writeWarningLine(cmd, "WARNING: "+warning)
	}
}

func writeHistoryContinuation(cmd *cobra.Command, page historystore.PageMetadata) {
	if page.HasMore {
		fmt.Fprintf(cmd.OutOrStdout(), "More results: rerun with the same filters and --cursor %s\n", page.NextCursor)
	}
}

func safePrettyText(value string) string {
	var result strings.Builder
	pendingCR := false
	for _, current := range value {
		if pendingCR {
			result.WriteByte('\n')
			pendingCR = false
			if current == '\n' {
				continue
			}
		}
		switch {
		case current == '\n' || current == '\t':
			result.WriteRune(current)
		case current == '\r':
			pendingCR = true
		case unicode.IsControl(current):
			fmt.Fprintf(&result, "\\u%04x", current)
		default:
			result.WriteRune(current)
		}
	}
	if pendingCR {
		result.WriteByte('\n')
	}
	return result.String()
}
