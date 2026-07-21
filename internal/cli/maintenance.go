package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/history/indexer"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/vault"
	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

const (
	lastAutoVaultMeta     = "last_auto_vault_unix"
	lastAutoVaultNanoMeta = "last_auto_vault_unix_nano"
)

type autoVaultResult struct {
	Ran       bool                          `json:"ran"`
	Archived  int                           `json:"archived"`
	Providers []vault.ArchiveProviderResult `json:"providers"`
	Warnings  []string                      `json:"warnings"`
}

type autoHistoryResult struct {
	Ran            bool `json:"ran"`
	ScannedSources int  `json:"scanned_sources"`
	IndexedSources int  `json:"indexed_sources"`
	IndexedPrompts int  `json:"indexed_prompts"`
	IndexedBundles int  `json:"indexed_vault_bundles"`
	ErrorCount     int  `json:"error_count"`
}

var dueHistoryIndex = runDueHistoryIndex

func autoVaultSummary(result autoVaultResult) string {
	parts := make([]string, 0, len(result.Providers))
	for _, provider := range result.Providers {
		if provider.Archived > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", provider.Provider, provider.Archived))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("auto-vault archived %d settled transcripts (%s)", result.Archived, strings.Join(parts, ", "))
}

func runDueAutoVault(cmd *cobra.Command, database *store.Store, roots []discover.Root) (autoVaultResult, error) {
	cfg := appconfig.FromContext(cmd.Context()).Config
	if !cfg.Vault.Auto {
		return autoVaultResult{}, nil
	}
	interval, _ := time.ParseDuration(cfg.Vault.AutoInterval)
	lastText, err := database.Meta(lastAutoVaultNanoMeta)
	if err != nil {
		return autoVaultResult{}, err
	}
	lastUnit := time.Nanosecond
	if lastText == "" {
		lastText, err = database.Meta(lastAutoVaultMeta)
		lastUnit = time.Second
		if err != nil {
			return autoVaultResult{}, err
		}
	}
	now := time.Now()
	if lastText != "" {
		last, parseErr := strconv.ParseInt(lastText, 10, 64)
		if parseErr != nil {
			return autoVaultResult{}, fmt.Errorf("parse last auto-vault time: %w", parseErr)
		}
		lastTime := time.Unix(0, last)
		if lastUnit == time.Second {
			lastTime = time.Unix(last, 0)
		}
		if now.Sub(lastTime) < interval {
			return autoVaultResult{}, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return autoVaultResult{}, fmt.Errorf("find user home directory: %w", err)
	}
	dir, err := configuredVaultDir(cfg, home)
	if err != nil {
		return autoVaultResult{}, err
	}
	providers := make([]discover.Provider, 0, len(cfg.Vault.Providers))
	for _, provider := range cfg.Vault.Providers {
		providers = append(providers, discover.Provider(provider))
	}
	minAge, _ := time.ParseDuration(cfg.Vault.MinAge)
	instance, err := vault.New(vault.Options{Dir: dir, Store: database, Roots: roots, Providers: providers, MinAge: minAge, Now: func() time.Time { return now }})
	if err != nil {
		return autoVaultResult{}, err
	}
	archive, err := instance.Archive(false)
	if err != nil {
		return autoVaultResult{}, err
	}
	result := autoVaultResult{Ran: true, Providers: archive.Providers, Warnings: archive.Warnings}
	for _, provider := range archive.Providers {
		result.Archived += provider.Archived
	}
	if err := database.Transaction(func(tx *store.Tx) error {
		if err := tx.SetMeta(lastAutoVaultMeta, strconv.FormatInt(now.Unix(), 10)); err != nil {
			return err
		}
		return tx.SetMeta(lastAutoVaultNanoMeta, strconv.FormatInt(now.UnixNano(), 10))
	}); err != nil {
		return autoVaultResult{}, fmt.Errorf("record auto-vault time: %w", err)
	}
	return result, nil
}

func autoVaultWarnings(result autoVaultResult, err error) []string {
	if err != nil {
		return []string{fmt.Sprintf("auto-vault transcripts: %v", err)}
	}
	warnings := make([]string, 0, len(result.Warnings))
	for _, warning := range result.Warnings {
		warnings = append(warnings, "auto-vault transcripts: "+warning)
	}
	return warnings
}

func writeAutoVaultDetails(cmd *cobra.Command, result autoVaultResult) {
	if !result.Ran {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Auto-vault")
	for _, provider := range result.Providers {
		fmt.Fprintf(cmd.OutOrStdout(), "  %-7s %d archived, %s input, %s stored, %d deduplicated, %d skipped\n",
			providerName(provider.Provider)+":", provider.Archived, humanBytes(provider.InputBytes), humanBytes(provider.StoredBytes), provider.Deduplicated, provider.Skipped)
	}
}

func runDueHistoryIndex(cmd *cobra.Command, roots []discover.Root) (autoHistoryResult, error) {
	cfg := appconfig.FromContext(cmd.Context()).Config
	if !cfg.History.AutoIndex {
		return autoHistoryResult{}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return autoHistoryResult{}, fmt.Errorf("find user home directory: %w", err)
	}
	stateDir, err := xdg.StateDir(xdg.Options{Home: home, Getenv: os.Getenv})
	if err != nil {
		return autoHistoryResult{}, err
	}
	historyPath := filepath.Join(stateDir, historystore.DatabaseName)
	health, err := historystore.InspectHealth(historyPath)
	if err != nil {
		return autoHistoryResult{}, err
	}
	interval, _ := time.ParseDuration(cfg.History.AutoInterval)
	attempt := time.Now()
	if health.LastAttemptUnix > 0 && attempt.Sub(time.Unix(health.LastAttemptUnix, 0)) < interval {
		return autoHistoryResult{}, nil
	}

	usageDatabase, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		return autoHistoryResult{}, fmt.Errorf("open usage store for history maintenance: %w", err)
	}
	defer usageDatabase.Close()
	vaultDir, err := configuredVaultDir(cfg, home)
	if err != nil {
		return autoHistoryResult{}, err
	}
	providers := make([]history.Provider, 0, len(cfg.History.Providers))
	vaultProviders := make([]discover.Provider, 0, len(cfg.History.Providers))
	for _, provider := range cfg.History.Providers {
		providers = append(providers, history.Provider(provider))
		vaultProviders = append(vaultProviders, discover.Provider(provider))
	}
	completeScope := historyMaintenanceCompleteScope(providers)
	minAge, _ := time.ParseDuration(cfg.Vault.MinAge)
	instance, vaultSetupErr := vault.New(vault.Options{Dir: vaultDir, Store: usageDatabase, Roots: roots, Providers: vaultProviders, MinAge: minAge, Now: func() time.Time { return attempt }})

	providerSummary := indexer.Summary{Errors: []indexer.Issue{}, Warnings: []indexer.Issue{}}
	vaultSummary := indexer.VaultSummary{Errors: []indexer.Issue{}, Warnings: []indexer.Issue{}}
	var providerErr, vaultErr error
	completedTogether := false
	markProviderError := func() {
		if providerErr != nil && providerSummary.ErrorCount == 0 {
			providerSummary.ErrorCount = 1
		}
	}
	markVaultError := func() {
		if vaultErr != nil && vaultSummary.ErrorCount == 0 {
			vaultSummary.ErrorCount = 1
		}
	}
	runProvider := func(database *historystore.Store) {
		providerSummary, providerErr = indexer.Index(indexer.Options{
			Store: database, Roots: roots, Providers: providers, Now: func() time.Time { return attempt }, LockHeld: true, SkipRunRecord: true,
		})
		markProviderError()
	}
	if vaultSetupErr == nil {
		vaultSummary, vaultErr = indexer.IndexVault(indexer.VaultOptions{
			StorePath: historyPath, Vault: instance, Providers: providers, Now: func() time.Time { return attempt }, SkipRunRecord: true,
			After: func(database *historystore.Store, current indexer.VaultSummary) error {
				completedTogether = true
				runProvider(database)
				return database.RecordScopedRun(attempt, providerSummary.ErrorCount+current.ErrorCount, completeScope)
			},
		})
	} else {
		vaultErr = vaultSetupErr
	}
	markVaultError()
	if !completedTogether {
		release, lockErr := historystore.Lock(historyPath)
		if lockErr != nil {
			return autoHistoryResult{}, errors.Join(vaultErr, lockErr)
		}
		database, openErr := historystore.Open(historyPath)
		if openErr != nil {
			release()
			return autoHistoryResult{}, errors.Join(vaultErr, openErr)
		}
		runProvider(database)
		recordErr := database.RecordScopedRun(attempt, providerSummary.ErrorCount+vaultSummary.ErrorCount, completeScope)
		closeErr := database.Close()
		release()
		if recordErr != nil || closeErr != nil {
			return autoHistoryResult{}, errors.Join(vaultErr, providerErr, recordErr, closeErr)
		}
	}
	result := autoHistoryResult{
		Ran: true, ScannedSources: providerSummary.ScannedSources, IndexedSources: providerSummary.IndexedSources,
		IndexedPrompts: providerSummary.IndexedPrompts + vaultSummary.IndexedPrompts,
		IndexedBundles: vaultSummary.IndexedBundles, ErrorCount: providerSummary.ErrorCount + vaultSummary.ErrorCount,
	}
	return result, errors.Join(providerErr, vaultErr)
}

func historyMaintenanceCompleteScope(providers []history.Provider) bool {
	selected := make(map[history.Provider]bool, len(providers))
	for _, provider := range providers {
		selected[provider] = true
	}
	return selected[history.ProviderCodex] && selected[history.ProviderClaude]
}

func autoHistoryWarnings(result autoHistoryResult, err error) []string {
	if err == nil {
		return []string{}
	}
	message := fmt.Sprintf("auto-index history: %v", err)
	message = strings.NewReplacer("\r", " ", "\n", " ").Replace(message)
	if len(message) > 512 {
		message = message[:512]
	}
	return []string{message}
}
