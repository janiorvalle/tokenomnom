package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/vault"
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
