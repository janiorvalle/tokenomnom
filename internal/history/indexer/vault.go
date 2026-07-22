package indexer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/ingest/jsonl"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/vault"
)

// VaultOptions configures one explicit immutable-vault indexing pass.
type VaultOptions struct {
	Store          *historystore.Store
	StorePath      string
	Vault          *vault.Vault
	Providers      []history.Provider
	Full           bool
	Now            func() time.Time
	LockHeld       bool
	SkipRunRecord  bool
	Before         func(*historystore.Store) error
	After          func(*historystore.Store, VaultSummary) error
	IndexAssistant bool
}

// VaultSummary reports archive-atomic backfill work.
type VaultSummary struct {
	SelectedBundles      int                        `json:"selected_bundles"`
	SelectedVersions     int                        `json:"selected_versions"`
	TraversedBundles     int                        `json:"traversed_bundles"`
	IndexedBundles       int                        `json:"indexed_bundles"`
	SkippedBundles       int                        `json:"skipped_bundles"`
	IndexedVersions      int                        `json:"indexed_versions"`
	IndexedPrompts       int                        `json:"indexed_prompts"`
	OversizedPrompts     int                        `json:"oversized_prompts"`
	ReclassifiedPrompts  int                        `json:"reclassified_prompts"`
	PromptKindCounts     map[history.PromptKind]int `json:"prompt_kind_counts"`
	BrokenSkippedBundles int                        `json:"broken_skipped_bundles"`
	ErrorCount           int                        `json:"error_count"`
	Errors               []Issue                    `json:"errors"`
	Warnings             []Issue                    `json:"warnings"`
	Full                 bool                       `json:"full"`
	Duration             time.Duration              `json:"-"`
}

// IndexVault verifies and reconciles every selected manifest version. Each
// bundle is decompressed once and committed in one history transaction.
func IndexVault(options VaultOptions) (VaultSummary, error) {
	started := time.Now()
	summary := VaultSummary{Errors: []Issue{}, Warnings: []Issue{}, Full: options.Full, PromptKindCounts: map[history.PromptKind]int{}}
	if options.Vault == nil || (options.Store == nil && options.StorePath == "") {
		return summary, errors.New("history store path and vault are required")
	}
	database := options.Store
	now := options.Now
	if now == nil {
		now = time.Now
	}
	attempt := now()
	providers := make([]discover.Provider, 0, len(options.Providers))
	for _, provider := range options.Providers {
		providers = append(providers, discover.Provider(provider))
	}
	acquire := func() (func(), error) {
		if options.LockHeld {
			if database == nil {
				return nil, errors.New("history store must be open when its lock is already held")
			}
			if err := database.ConfigureAssistantIndexing(options.IndexAssistant); err != nil {
				return nil, err
			}
			if options.Before != nil {
				if err := options.Before(database); err != nil {
					return nil, err
				}
			}
			return func() {}, nil
		}
		path := options.StorePath
		if path == "" {
			path = database.Path()
		}
		release, err := historystore.Lock(path)
		if err != nil {
			return nil, err
		}
		opened := false
		if database == nil {
			database, err = historystore.Open(path)
			if err != nil {
				release()
				return nil, err
			}
			opened = true
		}
		if err := database.ConfigureAssistantIndexing(options.IndexAssistant); err != nil {
			if opened {
				_ = database.Close()
			}
			release()
			return nil, err
		}
		if err := database.PrepareSampling(); err != nil {
			if opened {
				_ = database.Close()
			}
			release()
			return nil, fmt.Errorf("prepare history sampling index: %w", err)
		}
		if options.Before != nil {
			if err := options.Before(database); err != nil {
				if opened {
					_ = database.Close()
				}
				release()
				return nil, err
			}
		}
		return func() {
			if opened {
				_ = database.Close()
			}
			release()
		}, nil
	}
	var completeErr error
	walk, walkErr := options.Vault.WalkVerifiedBundlesComplete(vault.BundleQuery{
		Providers: providers,
		Skip: func(archive string, members []store.VaultFile) (bool, error) {
			if options.Full {
				return false, nil
			}
			current, err := database.VaultBundleCurrent(archive, bundleFingerprint(members), len(members))
			if current {
				summary.SkippedBundles++
			}
			return current, err
		},
	}, acquire, func(bundle *vault.BundleReader) error {
		members := bundle.Members()
		writer, err := database.BeginSnapshotBundle(bundle.Archive(), bundleFingerprint(members), len(members), options.Full, attempt)
		if err != nil {
			return err
		}
		defer writer.Rollback()
		bundleOversized := 0
		bundleReclassified := 0
		bundleKindCounts := map[history.PromptKind]int{}
		for {
			member, err := bundle.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			extraction, err := extractVaultMember(member, options.IndexAssistant)
			if err != nil {
				return err
			}
			if err := writer.Apply(historystore.SnapshotInput{
				Extraction: extraction,
				Snapshot: history.PreservedSnapshot{
					Provider: history.Provider(member.Manifest.Provider), ContentSHA256: member.Manifest.ContentSHA256,
					Size: member.Manifest.Size, FirstTS: extraction.Session.FirstTimestamp, LastTS: extraction.Session.LastTimestamp,
				},
			}); err != nil {
				return err
			}
			for _, diagnostic := range extraction.Diagnostics {
				addVaultIssue(&summary, Issue{
					Provider: string(member.Manifest.Provider), Path: member.Manifest.Archive + "#" + member.Manifest.RelPath,
					Error: fmt.Sprintf("line %d: %s", diagnostic.LineNumber, diagnostic.Message),
				}, false)
			}
			for _, prompt := range extraction.Prompts {
				kind := prompt.PromptKind
				if kind == "" {
					kind = history.ClassifyPromptKind(prompt.CleanText, prompt.Role, prompt.Classification)
				}
				bundleKindCounts[kind]++
				if isReclassifiedPrompt(prompt, kind) {
					bundleReclassified++
				}
				if prompt.Oversized {
					bundleOversized++
				}
			}
		}
		applied, err := writer.Commit()
		if err != nil {
			return err
		}
		if applied.Changed {
			summary.IndexedBundles++
			summary.IndexedVersions += applied.Snapshots
			summary.IndexedPrompts += applied.Prompts
			summary.OversizedPrompts += bundleOversized
			summary.ReclassifiedPrompts += bundleReclassified
			for kind, count := range bundleKindCounts {
				summary.PromptKindCounts[kind] += count
			}
		} else {
			summary.SkippedBundles++
		}
		return nil
	}, func(result vault.BundleWalkResult) (err error) {
		defer func() { completeErr = err }()
		summary.SelectedBundles = result.SelectedBundles
		summary.SelectedVersions = result.SelectedMembers
		summary.TraversedBundles = result.WalkedBundles
		for _, failure := range result.AllFailures {
			issue := Issue{Path: failure.Archive, Error: failure.Error}
			addVaultIssue(&summary, issue, true)
			if containsBrokenMarker(failure.Error) {
				summary.BrokenSkippedBundles++
			}
			if failure.Integrity {
				if err := database.RecordVaultBundleError(failure.Archive, attempt, errors.New(failure.Error)); err != nil {
					return err
				}
			} else if err := database.RecordVaultBundleIndexError(failure.Archive, attempt, errors.New(failure.Error)); err != nil {
				return err
			}
		}
		if !options.SkipRunRecord {
			// Vault-only is always a narrowed source scope and cannot vouch for
			// the provider half of last_complete_success.
			if err := database.RecordScopedRun(attempt, summary.ErrorCount, false); err != nil {
				return err
			}
		}
		if options.After != nil {
			return options.After(database, summary)
		}
		return nil
	})
	if completeErr != nil {
		return summary, completeErr
	}
	if walkErr != nil && walk.FailedBundles == 0 {
		return summary, walkErr
	}
	summary.Duration = time.Since(started)
	if summary.ErrorCount > 0 {
		return summary, PartialError{Count: summary.ErrorCount}
	}
	return summary, nil
}

func extractVaultMember(member vault.VerifiedMember, indexAssistant bool) (history.Extraction, error) {
	provider := history.Provider(member.Manifest.Provider)
	source := history.SourceReference{
		Provider: provider, Kind: history.LocationVault, Path: member.Manifest.SourcePath,
		RelativePath: member.Manifest.RelPath, Archive: member.Manifest.Archive, VaultVersion: member.Manifest.Version,
	}
	accumulator, err := newExtractionAccumulator(source, historystore.Checkpoint{}, fileNew, indexAssistant)
	if err != nil {
		return history.Extraction{}, err
	}
	_, err = jsonl.ReadPositionedReader(member.Content, jsonl.Position{}, accumulator.visit)
	if err != nil {
		return history.Extraction{}, err
	}
	extraction, _, err := accumulator.result()
	return extraction, err
}

func bundleFingerprint(files []store.VaultFile) string {
	type identity struct {
		SourcePath, Provider, RelPath, Archive, Hash, FirstTS, LastTS string
		Size, LineCount                                               int64
		Version                                                       int
	}
	values := make([]identity, 0, len(files))
	for _, file := range files {
		values = append(values, identity{
			SourcePath: file.SourcePath, Provider: string(file.Provider), RelPath: file.RelPath, Archive: file.Archive,
			Hash: file.ContentSHA256, FirstTS: file.FirstTS, LastTS: file.LastTS, Size: file.Size, LineCount: file.LineCount, Version: file.Version,
		})
	}
	encoded, _ := json.Marshal(values)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func containsBrokenMarker(value string) bool {
	return bytes.Contains([]byte(value), []byte("marked broken"))
}

func addVaultIssue(summary *VaultSummary, issue Issue, indexError bool) {
	if indexError {
		summary.ErrorCount++
		if len(summary.Errors) < maxIssues {
			summary.Errors = append(summary.Errors, issue)
		}
		return
	}
	if len(summary.Warnings) < maxIssues {
		summary.Warnings = append(summary.Warnings, issue)
	}
}
