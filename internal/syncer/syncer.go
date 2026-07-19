// Package syncer incrementally ingests discovered coding-agent transcripts.
package syncer

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/ingest"
	"github.com/janiorvalle/tokenomnom/internal/ingest/claude"
	"github.com/janiorvalle/tokenomnom/internal/ingest/codex"
	"github.com/janiorvalle/tokenomnom/internal/ingest/jsonl"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

const fingerprintWindow = int64(4096)

// Options configures one synchronization pass.
type Options struct {
	Store               *store.Store
	Roots               []discover.Root
	Location            *time.Location
	Timezone            string
	TimezoneFingerprint string
	Full                bool
	Now                 func() time.Time
	LockHeld            bool
}

// Summary reports work completed by one synchronization pass.
type Summary struct {
	FilesScanned                 int
	FilesSkipped                 int
	FilesAppended                int
	FilesRewritten               int
	FilesMissing                 int
	EventsApplied                int
	UsageRows                    int
	UnknownModelTokens           int64
	UnclassifiedCacheWriteTokens int64
	FullReingest                 bool
	Duration                     time.Duration
}

// Sync discovers and incrementally persists all complete JSONL records.
func Sync(options Options) (Summary, error) {
	started := time.Now()
	if options.Store == nil || options.Location == nil || options.Timezone == "" {
		return Summary{}, fmt.Errorf("store, location, and timezone are required")
	}
	release := func() {}
	if !options.LockHeld {
		var err error
		release, err = options.Store.LockSync()
		if err != nil {
			return Summary{}, err
		}
	}
	defer release()
	now := options.Now
	if now == nil {
		now = time.Now
	}
	timezoneFingerprint := options.TimezoneFingerprint
	if timezoneFingerprint == "" {
		timezoneFingerprint = options.Timezone
	}

	files, err := discoverFiles(options.Roots)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{FilesScanned: len(files)}
	seenPaths := make(map[string]bool, len(files))
	for _, file := range files {
		seenPaths[file.Path] = true
	}
	checkpoints, err := options.Store.Checkpoints()
	if err != nil {
		return Summary{}, err
	}
	processedCodex := make(map[string]fileKind)
	deferredSplits := make(map[string]string)
	aliases, deferredAliases, err := reconcileCodexMoves(options.Store, files, seenPaths, checkpoints, deferredSplits)
	if err != nil {
		return Summary{}, err
	}
	files = orderSplitOwnersFirst(files, deferredSplits)
	sourceFilesByPath := make(map[string]discover.SourceFile, len(files))
	for _, file := range files {
		sourceFilesByPath[file.Path] = file
	}
	missing, err := options.Store.MarkMissing(seenPaths)
	if err != nil {
		return Summary{}, err
	}
	summary.FilesMissing = missing

	info, err := options.Store.Info()
	if err != nil {
		return Summary{}, err
	}
	if info.PendingTimezone != "" && info.PendingTimezone != options.Timezone {
		return Summary{}, fmt.Errorf("incomplete timezone migration to %q; rerun with --tz %s", info.PendingTimezone, info.PendingTimezone)
	}
	timezoneUnset := info.Timezone == ""
	timezoneChanged := info.Timezone != "" && ((info.TimezoneFingerprint != "" && info.TimezoneFingerprint != timezoneFingerprint) ||
		(info.TimezoneFingerprint == "" && info.Timezone != options.Timezone))
	migrationPending := info.PendingTimezone != ""
	full := options.Full || timezoneChanged || migrationPending
	summary.FullReingest = full

	messages, err := options.Store.Messages()
	if err != nil {
		return Summary{}, err
	}
	if timezoneUnset || timezoneChanged || migrationPending {
		if err := options.Store.Transaction(func(tx *store.Tx) error {
			if err := tx.SetMeta("pending_timezone", options.Timezone); err != nil {
				return err
			}
			return tx.SetMeta("pending_timezone_fingerprint", timezoneFingerprint)
		}); err != nil {
			return Summary{}, err
		}
		if err := rebuildClaudeBuckets(options.Store, messages, options.Location); err != nil {
			return Summary{}, err
		}
		if err := options.Store.MarkMissingTimezoneStale(); err != nil {
			return Summary{}, err
		}
		// Present Codex files are reversed and re-bucketed below. Retained
		// contributions from vanished Codex files keep their original date
		// because their raw timestamps no longer exist.
	}
	for _, file := range files {
		if aliases[file.Path] {
			summary.FilesSkipped++
			continue
		}
		if ownerPath, split := deferredSplits[file.Path]; split {
			owner, ownerFound := checkpoints[ownerPath]
			if !ownerFound {
				return summary, fmt.Errorf("missing strict-prefix owner checkpoint %q", ownerPath)
			}
			checkpoint, splitErr := splitCheckpoint(file.Path, owner, ownerPath)
			if splitErr != nil {
				return summary, splitErr
			}
			if err := options.Store.Transaction(func(tx *store.Tx) error { return tx.PutCheckpoint(checkpoint) }); err != nil {
				return summary, err
			}
			checkpoints[file.Path] = checkpoint
		}
		checkpoint, found := checkpoints[file.Path]
		kind, err := classify(file, checkpoint, found)
		if err != nil {
			return summary, err
		}
		preserveKindOnFull := false
		aliasOf := checkpointAlias(checkpoint)
		if aliasOf != "" {
			ownerMissing := !seenPaths[aliasOf]
			splitContribution := checkpointSplitContribution(checkpoint)
			if splitContribution && kind == fileRewritten {
				matchesOwn, matchErr := fileMatchesCheckpoint(file, checkpoint)
				if matchErr != nil {
					return summary, matchErr
				}
				if matchesOwn {
					checkpoint.Size = file.Size
					checkpoint.ModTimeUnix = file.ModTime.UnixNano()
					checkpoint.Missing = false
					checkpoint.LastSyncedUnix = now().Unix()
					if err := options.Store.Transaction(func(tx *store.Tx) error { return tx.PutCheckpoint(checkpoint) }); err != nil {
						return summary, err
					}
					checkpoints[file.Path] = checkpoint
					kind = fileUnchanged
				} else if owner, ownerFound := checkpoints[aliasOf]; ownerFound {
					matchesOwner, ownerMatchErr := fileMatchesCheckpoint(file, owner)
					if ownerMatchErr != nil {
						return summary, ownerMatchErr
					}
					if matchesOwner {
						if err := options.Store.ResetFileContribution(file.Path); err != nil {
							return summary, err
						}
						checkpoint, err = splitCheckpoint(file.Path, owner, aliasOf)
						if err != nil {
							return summary, err
						}
						kind = fileAppended
						aliasOf = ""
						preserveKindOnFull = true
					}
				}
			}
			if ownerMissing && full && kind == fileUnchanged && !splitContribution {
				checkpoint, err = promoteCodexAlias(options.Store, file, checkpoint, aliasOf, true, checkpoints)
				if err != nil {
					return summary, err
				}
				aliasOf = ""
			}
			if ownerMissing && kind == fileAppended {
				checkpoint, err = markCheckpointSplit(checkpoint)
				if err != nil {
					return summary, err
				}
				aliasOf = ""
				preserveKindOnFull = true
			}
			if ownerMissing && kind == fileRewritten {
				matches, matchErr := fileMatchesCheckpoint(file, checkpoint)
				if matchErr != nil {
					return summary, matchErr
				}
				if matches {
					checkpoint, err = promoteCodexAlias(options.Store, file, checkpoint, aliasOf, true, checkpoints)
					if err != nil {
						return summary, err
					}
				} else {
					checkpoint, err = clearCheckpointAlias(checkpoint)
					if err != nil {
						return summary, err
					}
				}
				aliasOf = ""
			}
			if aliasOf != "" && kind == fileUnchanged && seenPaths[aliasOf] {
				owner, ownerFound := checkpoints[aliasOf]
				processedKind, ownerProcessed := processedCodex[aliasOf]
				ownerChanged := ownerProcessed && processedKind != fileUnchanged
				ownerRewritten := ownerProcessed && processedKind == fileRewritten
				contentsMatch := ownerFound && sameCheckpointContent(checkpoint, owner)
				persistedDivergence := ownerFound && checkpoint.Size == owner.Size && checkpoint.ByteOffset == owner.ByteOffset && !contentsMatch
				if ownerFound && !ownerProcessed {
					ownerKind, classifyErr := classify(sourceFilesByPath[aliasOf], owner, true)
					if classifyErr != nil {
						return summary, classifyErr
					}
					ownerChanged = ownerKind != fileUnchanged || full
					ownerRewritten = ownerKind == fileRewritten || full
					if ownerRewritten || (splitContribution && ownerChanged) {
						contentsMatch, err = sameContents(file.Path, aliasOf, make(map[string]string))
						if err != nil {
							return summary, err
						}
					}
				}
				if ownerChanged && contentsMatch && splitContribution {
					alias, aliasErr := aliasCheckpoint(file, owner, aliasOf)
					if aliasErr != nil {
						return summary, aliasErr
					}
					if err := options.Store.CollapseFile(file.Path, aliasOf, alias); err != nil {
						return summary, err
					}
					repointCheckpointAliases(checkpoints, file.Path, aliasOf)
					checkpoints[file.Path] = alias
					deferredAliases[file.Path] = aliasOf
					summary.FilesSkipped++
					continue
				}
				if !splitContribution && (ownerRewritten || persistedDivergence) && !contentsMatch {
					// The former duplicate still contains the contribution that
					// the owner's rewrite just reversed. Reingest it independently.
					aliasOf = ""
					kind = fileRewritten
				}
			}
			if aliasOf != "" && kind == fileUnchanged {
				if checkpoint.Missing {
					checkpoint.Missing = false
					checkpoint.LastSyncedUnix = now().Unix()
					if err := options.Store.Transaction(func(tx *store.Tx) error { return tx.PutCheckpoint(checkpoint) }); err != nil {
						return summary, err
					}
					checkpoints[file.Path] = checkpoint
				}
				summary.FilesSkipped++
				continue
			}
			if aliasOf != "" && seenPaths[aliasOf] {
				owner, ownerFound := checkpoints[aliasOf]
				ownerDivergedFromBase := ownerFound && !sameCheckpointContent(checkpoint, owner)
				ownerChanged := false
				if processedKind, ownerProcessed := processedCodex[aliasOf]; ownerProcessed {
					ownerChanged = processedKind != fileUnchanged
				} else if ownerFound {
					ownerKind, classifyErr := classify(sourceFilesByPath[aliasOf], owner, true)
					if classifyErr != nil {
						return summary, classifyErr
					}
					ownerChanged = ownerKind != fileUnchanged || full
				}
				if ownerFound && owner.Size == file.Size {
					equal, compareErr := sameContents(file.Path, aliasOf, make(map[string]string))
					if compareErr != nil {
						return summary, compareErr
					}
					if equal {
						if splitContribution {
							alias, aliasErr := aliasCheckpoint(file, owner, aliasOf)
							if aliasErr != nil {
								return summary, aliasErr
							}
							if err := options.Store.CollapseFile(file.Path, aliasOf, alias); err != nil {
								return summary, err
							}
							checkpoints[file.Path] = alias
						} else if err := putAliasCheckpoint(options.Store, file, owner, aliasOf, checkpoints); err != nil {
							return summary, err
						}
						summary.FilesSkipped++
						continue
					}
				}
				if kind == fileAppended && (ownerChanged || ownerDivergedFromBase) && !splitContribution {
					checkpoint, err = markCheckpointSplit(checkpoint)
					if err != nil {
						return summary, err
					}
					splitContribution = true
				}
				if splitContribution {
					if kind == fileRewritten {
						checkpoint, err = clearCheckpointAlias(checkpoint)
						if err != nil {
							return summary, err
						}
					}
					aliasOf = ""
					preserveKindOnFull = kind == fileAppended
				}
			}
			if aliasOf != "" {
				checkpoint, err = promoteCodexAlias(options.Store, file, checkpoint, aliasOf, !seenPaths[aliasOf], checkpoints)
				if err != nil {
					return summary, err
				}
			}
		}
		if full && found && !preserveKindOnFull {
			kind = fileRewritten
		}
		if kind == fileUnchanged {
			summary.FilesSkipped++
			if checkpoint.Missing {
				checkpoint.Missing = false
				checkpoint.LastSyncedUnix = now().Unix()
				if err := options.Store.Transaction(func(tx *store.Tx) error { return tx.PutCheckpoint(checkpoint) }); err != nil {
					return summary, err
				}
			}
			continue
		}
		if kind == fileAppended {
			summary.FilesAppended++
		}
		if kind == fileRewritten {
			summary.FilesRewritten++
		}

		switch file.Provider {
		case discover.ProviderCodex:
			events, parserState, parsed, parsedKind, stats, unknownIngested, err := parseCodex(file, checkpoint, kind, options.Location)
			if err != nil {
				return summary, err
			}
			if parsedKind != kind {
				summary.FilesAppended--
				summary.FilesRewritten++
				kind = parsedKind
			}
			parserState.PrefixHash = parsed.prefixHash
			parserState.PrefixHashState = parsed.prefixHashState
			stateJSON, err := json.Marshal(parserState)
			if err != nil {
				return summary, fmt.Errorf("encode Codex parser state: %w", err)
			}
			if kind == fileRewritten && found && checkpointAlias(checkpoint) == "" {
				matches, matchErr := fileMatchesCheckpoint(file, checkpoint)
				if matchErr != nil {
					return summary, matchErr
				}
				if !matches {
					if err := preserveMissingAliasContribution(options.Store, file.Path, seenPaths, checkpoints); err != nil {
						return summary, err
					}
				}
			}
			syncedAt := now()
			if err := persistCodexFile(options.Store, parsed, string(stateJSON), kind, events, syncedAt); err != nil {
				return summary, err
			}
			checkpoints[file.Path] = checkpointFor(parsed.source, parsed.offset, parsed.hash, string(stateJSON), syncedAt)
			processedCodex[file.Path] = kind
			summary.EventsApplied += stats.EmittedEvents
			summary.UnknownModelTokens += unknownIngested
			addDiagnostics(&summary, events)
		case discover.ProviderClaude:
			candidates, parsed, parsedKind, err := parseClaude(file, checkpoint, kind)
			if err != nil {
				return summary, err
			}
			if parsedKind != kind {
				summary.FilesAppended--
				summary.FilesRewritten++
				kind = parsedKind
			}
			applied, diagnostics, err := persistClaudeFile(options.Store, parsed, candidates, messages, options.Location, now())
			if err != nil {
				return summary, err
			}
			summary.EventsApplied += applied
			summary.UnknownModelTokens += diagnostics.UnknownModelTokens
			summary.UnclassifiedCacheWriteTokens += diagnostics.UnclassifiedCacheWriteTokens
		default:
			return summary, fmt.Errorf("unsupported provider %q", file.Provider)
		}
	}
	if len(deferredAliases) > 0 {
		checkpoints, err = options.Store.Checkpoints()
		if err != nil {
			return summary, err
		}
		filesByPath := make(map[string]discover.SourceFile, len(files))
		for _, file := range files {
			filesByPath[file.Path] = file
		}
		for aliasPath, ownerPath := range deferredAliases {
			owner, found := checkpoints[ownerPath]
			if !found {
				return summary, fmt.Errorf("missing duplicate owner checkpoint %q", ownerPath)
			}
			if err := putAliasCheckpoint(options.Store, filesByPath[aliasPath], owner, ownerPath, checkpoints); err != nil {
				return summary, err
			}
		}
	}

	finished := now()
	if err := options.Store.Transaction(func(tx *store.Tx) error {
		if err := tx.SetMeta("timezone", options.Timezone); err != nil {
			return err
		}
		if err := tx.SetMeta("timezone_fingerprint", timezoneFingerprint); err != nil {
			return err
		}
		if err := tx.SetMeta("last_sync_unix", strconv.FormatInt(finished.Unix(), 10)); err != nil {
			return err
		}
		if err := tx.DeleteMeta("pending_timezone"); err != nil {
			return err
		}
		return tx.DeleteMeta("pending_timezone_fingerprint")
	}); err != nil {
		return summary, err
	}
	info, err = options.Store.Info()
	if err != nil {
		return summary, err
	}
	summary.UsageRows = info.UsageRows
	summary.Duration = time.Since(started)
	return summary, nil
}

func promoteCodexAlias(database *store.Store, file discover.SourceFile, alias store.Checkpoint, ownerPath string, ownerMissing bool, checkpoints map[string]store.Checkpoint) (store.Checkpoint, error) {
	owner, found := checkpoints[ownerPath]
	if !found {
		return store.Checkpoint{}, fmt.Errorf("missing contribution owner %q for alias %q", ownerPath, file.Path)
	}
	var newOwnerState codex.State
	if alias.ParserState != "" {
		if err := json.Unmarshal([]byte(alias.ParserState), &newOwnerState); err != nil {
			return store.Checkpoint{}, err
		}
	}
	newOwnerState.AliasOf = ""
	newOwnerJSON, err := json.Marshal(newOwnerState)
	if err != nil {
		return store.Checkpoint{}, err
	}
	newOwner := alias
	newOwner.ParserState = string(newOwnerJSON)
	newOwner.Missing = false

	ownerAlias := owner
	var oldOwnerState codex.State
	if owner.ParserState != "" {
		if err := json.Unmarshal([]byte(owner.ParserState), &oldOwnerState); err != nil {
			return store.Checkpoint{}, err
		}
	}
	oldOwnerState.AliasOf = file.Path
	aliasJSON, err := json.Marshal(oldOwnerState)
	if err != nil {
		return store.Checkpoint{}, err
	}
	ownerAlias.ParserState = string(aliasJSON)
	ownerAlias.Missing = ownerMissing
	if err := database.PromoteAlias(file.Path, ownerPath, newOwner, ownerAlias); err != nil {
		return store.Checkpoint{}, err
	}
	repointCheckpointAliases(checkpoints, ownerPath, file.Path)
	delete(checkpoints, ownerPath)
	checkpoints[file.Path] = newOwner
	checkpoints[ownerPath] = ownerAlias
	return newOwner, nil
}

func reconcileCodexMoves(database *store.Store, files []discover.SourceFile, seen map[string]bool, checkpoints map[string]store.Checkpoint, deferredSplits map[string]string) (map[string]bool, map[string]string, error) {
	aliases := make(map[string]bool)
	deferredAliases := make(map[string]string)
	sources := make(map[string]discover.SourceFile, len(files))
	contentHashes := make(map[string]string)
	prefixHashes := make(map[string]string)
	newGroups := make(map[string][]discover.SourceFile)
	newByBase := make(map[string][]discover.SourceFile)
	presentGroups := make(map[string][]discover.SourceFile)
	for _, file := range files {
		sources[file.Path] = file
		if file.Provider == discover.ProviderCodex {
			presentGroups[strconv.FormatInt(file.Size, 10)] = append(presentGroups[strconv.FormatInt(file.Size, 10)], file)
			if _, found := checkpoints[file.Path]; !found {
				key := strconv.FormatInt(file.Size, 10)
				newGroups[key] = append(newGroups[key], file)
				newByBase[filepath.Base(file.Path)] = append(newByBase[filepath.Base(file.Path)], file)
			}
		}
	}
	for _, group := range newByBase {
		sort.Slice(group, func(i, j int) bool { return group[i].Size < group[j].Size })
		for i := 1; i < len(group); i++ {
			for j := i - 1; j >= 0; j-- {
				if group[j].Size >= group[i].Size {
					continue
				}
				shortHash, err := fullHash(group[j].Path, contentHashes)
				if err != nil {
					return nil, nil, err
				}
				longPrefix, err := prefixHash(group[i].Path, group[j].Size, prefixHashes)
				if err != nil {
					return nil, nil, err
				}
				if shortHash == longPrefix {
					deferredSplits[group[i].Path] = group[j].Path
					break
				}
			}
		}
	}
	for _, sizeGroup := range presentGroups {
		if len(sizeGroup) < 2 {
			continue
		}
		var ownerFiles []discover.SourceFile
		ownerChanged := false
		for _, file := range sizeGroup {
			checkpoint, found := checkpoints[file.Path]
			if !found || checkpointAlias(checkpoint) != "" {
				continue
			}
			ownerFiles = append(ownerFiles, file)
			kind, err := classify(file, checkpoint, true)
			if err != nil {
				return nil, nil, err
			}
			ownerChanged = ownerChanged || kind != fileUnchanged
		}
		if len(ownerFiles) < 2 || !ownerChanged {
			continue
		}
		byHash := make(map[string][]discover.SourceFile)
		for _, file := range ownerFiles {
			hash, err := fullHash(file.Path, contentHashes)
			if err != nil {
				return nil, nil, err
			}
			byHash[hash] = append(byHash[hash], file)
		}
		for _, identical := range byHash {
			var owners []string
			for _, file := range identical {
				checkpoint, found := checkpoints[file.Path]
				if found && checkpointAlias(checkpoint) == "" {
					owners = append(owners, file.Path)
				}
			}
			if len(owners) < 2 {
				continue
			}
			sort.Strings(owners)
			ownerPath := owners[0]
			owner := checkpoints[ownerPath]
			for _, duplicatePath := range owners[1:] {
				alias, err := aliasCheckpoint(sources[duplicatePath], owner, ownerPath)
				if err != nil {
					return nil, nil, err
				}
				if err := database.CollapseFile(duplicatePath, ownerPath, alias); err != nil {
					return nil, nil, err
				}
				repointCheckpointAliases(checkpoints, duplicatePath, ownerPath)
				checkpoints[duplicatePath] = alias
				aliases[duplicatePath] = true
				deferredAliases[duplicatePath] = ownerPath
			}
		}
	}
	for _, group := range newGroups {
		if len(group) < 2 {
			continue
		}
		canonicalByHash := make(map[string]string)
		for _, file := range group {
			hash, err := fullHash(file.Path, contentHashes)
			if err != nil {
				return nil, nil, err
			}
			if _, found := canonicalByHash[hash]; found {
				aliases[file.Path] = true
				deferredAliases[file.Path] = canonicalByHash[hash]
				continue
			}
			canonicalByHash[hash] = file.Path
		}
	}
	for _, file := range files {
		if file.Provider != discover.ProviderCodex {
			continue
		}
		if _, found := checkpoints[file.Path]; found {
			continue
		}
		if aliases[file.Path] {
			continue
		}
		var missingMatches []string
		var presentMatches []string
		presentSplit := ""
		var presentSplitOffset int64
		for oldPath, checkpoint := range checkpoints {
			if checkpoint.Provider != file.Provider {
				continue
			}
			if seen[oldPath] {
				oldFile := sources[oldPath]
				if oldFile.Size == file.Size {
					equal, err := sameContents(oldPath, file.Path, contentHashes)
					if err != nil {
						return nil, nil, err
					}
					if equal {
						presentMatches = append(presentMatches, oldPath)
					}
				}
				if checkpointAlias(checkpoint) != "" || checkpoint.ByteOffset >= file.Size || checkpoint.ByteOffset <= presentSplitOffset {
					continue
				}
				wantHash := checkpointPrefixHash(checkpoint)
				if wantHash == "" {
					continue
				}
				hash, err := prefixHash(file.Path, checkpoint.ByteOffset, prefixHashes)
				if err != nil {
					return nil, nil, err
				}
				if hash == wantHash {
					presentSplit = oldPath
					presentSplitOffset = checkpoint.ByteOffset
				}
				continue
			}
			if checkpoint.ByteOffset > file.Size {
				continue
			}
			wantHash := checkpointPrefixHash(checkpoint)
			if wantHash == "" {
				continue
			}
			hash, err := prefixHash(file.Path, checkpoint.ByteOffset, prefixHashes)
			if err != nil {
				return nil, nil, err
			}
			if hash != wantHash {
				continue
			}
			missingMatches = append(missingMatches, oldPath)
		}
		oldPath := uniqueMissingOwner(missingMatches, checkpoints)
		if oldPath != "" {
			if err := database.MoveFile(oldPath, file.Path); err != nil {
				return nil, nil, err
			}
			checkpoint := checkpoints[oldPath]
			delete(checkpoints, oldPath)
			checkpoint.Path = file.Path
			checkpoint.Missing = false
			checkpoints[file.Path] = checkpoint
			repointCheckpointAliases(checkpoints, oldPath, file.Path)
			continue
		}
		if presentSplit != "" {
			deferredSplits[file.Path] = presentSplit
			continue
		}
		// Copies share one contribution owner. Prefer the lexicographically first
		// path, which naturally favors archived_sessions over sessions.
		if len(presentMatches) > 0 {
			sort.Strings(presentMatches)
			owner := presentMatches[0]
			if file.Path < owner {
				if err := database.MoveFile(owner, file.Path); err != nil {
					return nil, nil, err
				}
				checkpoint := checkpoints[owner]
				delete(checkpoints, owner)
				checkpoint.Path = file.Path
				checkpoint.Missing = false
				checkpoints[file.Path] = checkpoint
				repointCheckpointAliases(checkpoints, owner, file.Path)
				if err := putAliasCheckpoint(database, sources[owner], checkpoint, file.Path, checkpoints); err != nil {
					return nil, nil, err
				}
				aliases[owner] = true
			} else {
				if err := putAliasCheckpoint(database, file, checkpoints[owner], owner, checkpoints); err != nil {
					return nil, nil, err
				}
				aliases[file.Path] = true
			}
		}
	}
	return aliases, deferredAliases, nil
}

func orderSplitOwnersFirst(files []discover.SourceFile, splits map[string]string) []discover.SourceFile {
	byPath := make(map[string]discover.SourceFile, len(files))
	for _, file := range files {
		byPath[file.Path] = file
	}
	ordered := make([]discover.SourceFile, 0, len(files))
	visited := make(map[string]bool)
	visiting := make(map[string]bool)
	var visit func(string)
	visit = func(path string) {
		if visited[path] || visiting[path] {
			return
		}
		visiting[path] = true
		if ownerPath, found := splits[path]; found {
			if _, present := byPath[ownerPath]; present {
				visit(ownerPath)
			}
		}
		visiting[path] = false
		visited[path] = true
		if file, found := byPath[path]; found {
			ordered = append(ordered, file)
		}
	}
	for _, file := range files {
		visit(file.Path)
	}
	return ordered
}

func uniqueMissingOwner(matches []string, checkpoints map[string]store.Checkpoint) string {
	if len(matches) == 1 {
		return matches[0]
	}
	owner := ""
	for _, path := range matches {
		if checkpointAlias(checkpoints[path]) != "" {
			continue
		}
		if owner != "" {
			return ""
		}
		owner = path
	}
	return owner
}

func repointCheckpointAliases(checkpoints map[string]store.Checkpoint, oldOwner, newOwner string) {
	for path, checkpoint := range checkpoints {
		if checkpointAlias(checkpoint) != oldOwner {
			continue
		}
		var state codex.State
		if err := json.Unmarshal([]byte(checkpoint.ParserState), &state); err != nil {
			continue
		}
		state.AliasOf = newOwner
		encoded, err := json.Marshal(state)
		if err != nil {
			continue
		}
		checkpoint.ParserState = string(encoded)
		checkpoints[path] = checkpoint
	}
}

func putAliasCheckpoint(database *store.Store, aliasFile discover.SourceFile, owner store.Checkpoint, ownerPath string, checkpoints map[string]store.Checkpoint) error {
	alias, err := aliasCheckpoint(aliasFile, owner, ownerPath)
	if err != nil {
		return err
	}
	if err := database.Transaction(func(tx *store.Tx) error { return tx.PutCheckpoint(alias) }); err != nil {
		return err
	}
	checkpoints[alias.Path] = alias
	return nil
}

func aliasCheckpoint(aliasFile discover.SourceFile, owner store.Checkpoint, ownerPath string) (store.Checkpoint, error) {
	var state codex.State
	if owner.ParserState != "" {
		if err := json.Unmarshal([]byte(owner.ParserState), &state); err != nil {
			return store.Checkpoint{}, fmt.Errorf("decode duplicate owner state %q: %w", ownerPath, err)
		}
	}
	state.AliasOf = ownerPath
	encoded, err := json.Marshal(state)
	if err != nil {
		return store.Checkpoint{}, err
	}
	alias := owner
	alias.Path = aliasFile.Path
	alias.Size = aliasFile.Size
	alias.ModTimeUnix = aliasFile.ModTime.UnixNano()
	alias.ParserState = string(encoded)
	alias.Missing = false
	return alias, nil
}

func checkpointAlias(checkpoint store.Checkpoint) string {
	if checkpoint.ParserState == "" || checkpoint.Provider != discover.ProviderCodex {
		return ""
	}
	var state codex.State
	if err := json.Unmarshal([]byte(checkpoint.ParserState), &state); err != nil {
		return ""
	}
	return state.AliasOf
}

func clearCheckpointAlias(checkpoint store.Checkpoint) (store.Checkpoint, error) {
	if checkpoint.ParserState == "" {
		return checkpoint, nil
	}
	var state codex.State
	if err := json.Unmarshal([]byte(checkpoint.ParserState), &state); err != nil {
		return store.Checkpoint{}, fmt.Errorf("decode Codex alias state for %q: %w", checkpoint.Path, err)
	}
	state.AliasOf = ""
	state.SplitContribution = false
	encoded, err := json.Marshal(state)
	if err != nil {
		return store.Checkpoint{}, err
	}
	checkpoint.ParserState = string(encoded)
	return checkpoint, nil
}

func markCheckpointSplit(checkpoint store.Checkpoint) (store.Checkpoint, error) {
	if checkpoint.ParserState == "" {
		return checkpoint, fmt.Errorf("missing Codex parser state for split contribution %q", checkpoint.Path)
	}
	var state codex.State
	if err := json.Unmarshal([]byte(checkpoint.ParserState), &state); err != nil {
		return store.Checkpoint{}, fmt.Errorf("decode Codex split state for %q: %w", checkpoint.Path, err)
	}
	state.SplitContribution = true
	encoded, err := json.Marshal(state)
	if err != nil {
		return store.Checkpoint{}, err
	}
	checkpoint.ParserState = string(encoded)
	return checkpoint, nil
}

func splitCheckpoint(path string, owner store.Checkpoint, ownerPath string) (store.Checkpoint, error) {
	checkpoint := owner
	checkpoint.Path = path
	checkpoint.Size = owner.ByteOffset
	checkpoint.ModTimeUnix = 0
	checkpoint.Missing = false
	if checkpoint.ParserState == "" {
		return store.Checkpoint{}, fmt.Errorf("missing Codex owner parser state %q", ownerPath)
	}
	var state codex.State
	if err := json.Unmarshal([]byte(checkpoint.ParserState), &state); err != nil {
		return store.Checkpoint{}, err
	}
	state.AliasOf = ownerPath
	state.SplitContribution = true
	encoded, err := json.Marshal(state)
	if err != nil {
		return store.Checkpoint{}, err
	}
	checkpoint.ParserState = string(encoded)
	return checkpoint, nil
}

func checkpointSplitContribution(checkpoint store.Checkpoint) bool {
	if checkpoint.ParserState == "" || checkpoint.Provider != discover.ProviderCodex {
		return false
	}
	var state codex.State
	return json.Unmarshal([]byte(checkpoint.ParserState), &state) == nil && state.SplitContribution
}

func fileMatchesCheckpoint(file discover.SourceFile, checkpoint store.Checkpoint) (bool, error) {
	want := checkpointPrefixHash(checkpoint)
	if want == "" || checkpoint.ByteOffset > file.Size {
		return false, nil
	}
	got, err := prefixHash(file.Path, checkpoint.ByteOffset, make(map[string]string))
	if err != nil {
		return false, err
	}
	return got == want, nil
}

func preserveMissingAliasContribution(database *store.Store, ownerPath string, seen map[string]bool, checkpoints map[string]store.Checkpoint) error {
	var candidates []string
	for path, checkpoint := range checkpoints {
		if !seen[path] && checkpointAlias(checkpoint) == ownerPath && !checkpointSplitContribution(checkpoint) {
			candidates = append(candidates, path)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Strings(candidates)
	retainedPath := candidates[0]
	retained, err := clearCheckpointAlias(checkpoints[retainedPath])
	if err != nil {
		return err
	}
	retained.Missing = true
	if err := database.PreserveContribution(ownerPath, retainedPath, retained); err != nil {
		return err
	}
	repointCheckpointAliases(checkpoints, ownerPath, retainedPath)
	checkpoints[retainedPath] = retained
	return nil
}

func checkpointPrefixHash(checkpoint store.Checkpoint) string {
	if checkpoint.ParserState == "" || checkpoint.Provider != discover.ProviderCodex {
		return ""
	}
	var state codex.State
	if err := json.Unmarshal([]byte(checkpoint.ParserState), &state); err != nil {
		return ""
	}
	return state.PrefixHash
}

func sameCheckpointContent(first, second store.Checkpoint) bool {
	firstHash := checkpointPrefixHash(first)
	secondHash := checkpointPrefixHash(second)
	return firstHash != "" && first.Size == second.Size && first.ByteOffset == second.ByteOffset && firstHash == secondHash
}

func sameContents(firstPath, secondPath string, hashes map[string]string) (bool, error) {
	first, err := fullHash(firstPath, hashes)
	if err != nil {
		return false, err
	}
	second, err := fullHash(secondPath, hashes)
	if err != nil {
		return false, err
	}
	return first == second, nil
}

func fullHash(path string, hashes map[string]string) (string, error) {
	if value, found := hashes[path]; found {
		return value, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %q for duplicate detection: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash %q for duplicate detection: %w", path, err)
	}
	value := hex.EncodeToString(hash.Sum(nil))
	hashes[path] = value
	return value, nil
}

func prefixHash(path string, offset int64, hashes map[string]string) (string, error) {
	key := path + ":" + strconv.FormatInt(offset, 10)
	if value, found := hashes[key]; found {
		return value, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %q for move detection: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.CopyN(hash, file, offset); err != nil {
		return "", fmt.Errorf("hash prefix of %q for move detection: %w", path, err)
	}
	value := hex.EncodeToString(hash.Sum(nil))
	hashes[key] = value
	return value, nil
}

func discoverFiles(roots []discover.Root) ([]discover.SourceFile, error) {
	var files []discover.SourceFile
	for _, root := range roots {
		found, walkErrors := discover.ListSourceFiles(root)
		if len(walkErrors) > 0 {
			return nil, fmt.Errorf("discover %s transcripts: %v", root.Provider, walkErrors)
		}
		files = append(files, found...)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

type fileKind int

const (
	fileNew fileKind = iota
	fileUnchanged
	fileAppended
	fileRewritten
)

func classify(file discover.SourceFile, checkpoint store.Checkpoint, found bool) (fileKind, error) {
	if !found {
		return fileNew, nil
	}
	if checkpoint.LastSyncedUnix < 0 {
		return fileRewritten, nil
	}
	if file.Size == checkpoint.Size && file.ModTime.UnixNano() == checkpoint.ModTimeUnix {
		if checkpoint.Missing {
			got, err := tailHash(file.Path, checkpoint.ByteOffset)
			if err != nil {
				return fileRewritten, err
			}
			if got != checkpoint.TailHash {
				return fileRewritten, nil
			}
		}
		return fileUnchanged, nil
	}
	if file.Size > checkpoint.Size && checkpoint.ByteOffset <= file.Size {
		got, err := tailHash(file.Path, checkpoint.ByteOffset)
		if err != nil {
			return fileRewritten, err
		}
		if got == checkpoint.TailHash {
			return fileAppended, nil
		}
	}
	return fileRewritten, nil
}

func parseCodex(file discover.SourceFile, checkpoint store.Checkpoint, kind fileKind, location *time.Location) (map[usageKey]store.Usage, codex.State, parsedFile, fileKind, ingest.Stats, int64, error) {
	offset := int64(0)
	state := codex.State{}
	if kind == fileAppended {
		offset = checkpoint.ByteOffset
		if checkpoint.ParserState != "" {
			if err := json.Unmarshal([]byte(checkpoint.ParserState), &state); err != nil {
				return nil, state, parsedFile{}, kind, ingest.Stats{}, 0, fmt.Errorf("decode Codex parser state for %q: %w", file.Path, err)
			}
		}
	}
	events := make(map[usageKey]store.Usage)
	priorPending := len(state.Pending)
	var priorPendingTokens int64
	if kind == fileAppended {
		for _, event := range state.Pending {
			if event.Input+event.Output > 0 {
				priorPendingTokens += event.Input + event.Output
			}
			event.Model = "unknown"
			negateEvent(&event)
			aggregate(events, event, location)
		}
	}
	var unknownIngested int64
	parser := codex.NewParser(state, func(event ingest.UsageEvent) {
		if event.Model == "unknown" && event.Input+event.Output > 0 {
			unknownIngested += event.Input + event.Output
		}
		aggregate(events, event, location)
	})
	expectedPrefix := ""
	if kind == fileAppended {
		expectedPrefix = checkpoint.TailHash
	}
	parsed, err := readParsedFile(file, offset, expectedPrefix, &state.PrefixHashState, parser.ParseLine)
	if errors.Is(err, errSourceRewritten) && kind == fileAppended {
		return parseCodex(file, checkpoint, fileRewritten, location)
	}
	if err != nil {
		return nil, state, parsedFile{}, kind, parser.Stats(), 0, err
	}
	savedState := parser.State()
	if len(savedState.Pending) > 0 {
		parser.FlushUnknown()
		unknownIngested -= priorPendingTokens
		if unknownIngested < 0 {
			unknownIngested = 0
		}
	}
	stats := parser.Stats()
	stats.EmittedEvents -= priorPending
	if stats.UnknownModelEvents >= priorPending {
		stats.UnknownModelEvents -= priorPending
	}
	return events, savedState, parsed, kind, stats, unknownIngested, nil
}

func parseClaude(file discover.SourceFile, checkpoint store.Checkpoint, kind fileKind) ([]claude.Candidate, parsedFile, fileKind, error) {
	offset := int64(0)
	if kind == fileAppended {
		offset = checkpoint.ByteOffset
	}
	deduper := claude.NewDeduper()
	var stats claude.FileStats
	expectedPrefix := ""
	if kind == fileAppended {
		expectedPrefix = checkpoint.TailHash
	}
	parsed, err := readParsedFile(file, offset, expectedPrefix, nil, func(line []byte) {
		claude.ParseLine(line, deduper.Add, &stats)
	})
	if errors.Is(err, errSourceRewritten) && kind == fileAppended {
		return parseClaude(file, checkpoint, fileRewritten)
	}
	var candidates []claude.Candidate
	deduper.Retained(func(candidate claude.Candidate) { candidates = append(candidates, candidate) })
	return candidates, parsed, kind, err
}

type parsedFile struct {
	source          discover.SourceFile
	offset          int64
	hash            string
	prefixHash      string
	prefixHashState string
}

var errSourceRewritten = errors.New("source prefix changed before parsing")

func readParsedFile(source discover.SourceFile, offset int64, expectedPrefix string, prefixState *string, visit func([]byte)) (parsedFile, error) {
	file, err := os.Open(source.Path)
	if err != nil {
		return parsedFile{}, fmt.Errorf("open JSONL file %q: %w", source.Path, err)
	}
	defer file.Close()
	if expectedPrefix != "" {
		got, err := tailHashFile(file, offset)
		if err != nil {
			return parsedFile{}, err
		}
		if got != expectedPrefix {
			return parsedFile{}, errSourceRewritten
		}
	}
	var prefixHasher hashState
	if prefixState != nil {
		prefixHasher, err = newHashState(file, offset, *prefixState)
		if err != nil {
			return parsedFile{}, err
		}
	}
	trackedVisit := visit
	if prefixHasher != nil {
		trackedVisit = func(line []byte) {
			_, _ = prefixHasher.Write(line)
			visit(line)
		}
	}
	end, err := jsonl.ReadCompleteFile(file, offset, trackedVisit)
	if err != nil {
		return parsedFile{}, err
	}
	observedEOF, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return parsedFile{}, fmt.Errorf("read observed EOF for %q: %w", source.Path, err)
	}
	parsedInfo, err := file.Stat()
	if err != nil {
		return parsedFile{}, fmt.Errorf("stat parsed JSONL file %q: %w", source.Path, err)
	}
	hash, err := tailHashFile(file, end)
	if err != nil {
		return parsedFile{}, err
	}
	if observedEOF < end {
		observedEOF = end
	}
	modTime := parsedInfo.ModTime()
	if parsedInfo.Size() != observedEOF {
		// Bytes arrived after the reader observed EOF. Keep the parsed size
		// so the next pass sees an append, without pairing it to a newer mtime.
		modTime = time.Time{}
	}
	parsed := parsedFile{source: discover.SourceFile{
		Provider: source.Provider, Path: source.Path, Size: observedEOF, ModTime: modTime,
	}, offset: end, hash: hash}
	if prefixHasher != nil {
		parsed.prefixHash = hex.EncodeToString(prefixHasher.Sum(nil))
		encoded, err := prefixHasher.MarshalBinary()
		if err != nil {
			return parsedFile{}, fmt.Errorf("encode prefix hash state for %q: %w", source.Path, err)
		}
		parsed.prefixHashState = hex.EncodeToString(encoded)
	}
	return parsed, nil
}

type hashState interface {
	io.Writer
	Sum([]byte) []byte
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
}

func newHashState(file *os.File, offset int64, encoded string) (hashState, error) {
	hasher, ok := sha256.New().(hashState)
	if !ok {
		return nil, fmt.Errorf("SHA-256 implementation does not support resumable state")
	}
	if encoded != "" {
		state, err := hex.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode prefix hash state for %q: %w", file.Name(), err)
		}
		if err := hasher.UnmarshalBinary(state); err != nil {
			return nil, fmt.Errorf("restore prefix hash state for %q: %w", file.Name(), err)
		}
		return hasher, nil
	}
	if offset > 0 {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		if _, err := io.CopyN(hasher, file, offset); err != nil {
			return nil, fmt.Errorf("hash existing prefix for %q: %w", file.Name(), err)
		}
	}
	return hasher, nil
}

type usageKey struct{ date, provider, model string }

func aggregate(values map[usageKey]store.Usage, event ingest.UsageEvent, location *time.Location) {
	date := event.Timestamp.In(location).Format("2006-01-02")
	key := usageKey{date, string(event.Provider), event.Model}
	value := values[key]
	value.Date, value.Provider, value.Model = date, event.Provider, event.Model
	value.Input += event.Input
	value.CacheRead += event.CacheRead
	value.CacheWrite5m += event.CacheWrite5m
	value.CacheWrite1h += event.CacheWrite1h
	value.CacheWriteUnclassified += event.CacheWriteUnclassified
	value.Output += event.Output
	value.Reasoning += event.Reasoning
	values[key] = value
}

func persistCodexFile(database *store.Store, parsed parsedFile, parserState string, kind fileKind, events map[usageKey]store.Usage, now time.Time) error {
	file := parsed.source
	return database.Transaction(func(tx *store.Tx) error {
		if kind == fileRewritten {
			if err := tx.ReverseFile(file.Path); err != nil {
				return fmt.Errorf("reverse rewritten Codex file %q: %w", file.Path, err)
			}
		}
		for _, value := range events {
			if err := tx.ApplyUsage(value, file.Path); err != nil {
				return err
			}
		}
		return tx.PutCheckpoint(checkpointFor(file, parsed.offset, parsed.hash, parserState, now))
	})
}

type diagnostics struct {
	UnknownModelTokens           int64
	UnclassifiedCacheWriteTokens int64
}

func persistClaudeFile(database *store.Store, parsed parsedFile, candidates []claude.Candidate, messages map[string]store.Message, location *time.Location, now time.Time) (int, diagnostics, error) {
	file := parsed.source
	var err error
	type change struct {
		message store.Message
		usages  []store.Usage
		oldUses []store.Usage
	}
	var changes []change
	working := make(map[string]store.Message)
	for _, candidate := range candidates {
		existing, found := working[candidate.MessageID]
		if !found {
			existing, found = messages[candidate.MessageID]
		}
		// Stored authority remains the baseline even for --full: the highest
		// snapshot may survive only in a transcript that Claude already deleted.
		if found && candidate.Score <= existing.Score {
			continue
		}
		timestamp := candidate.Timestamp
		var oldUses []store.Usage
		if found {
			oldUses, err = messageUsages(existing, location)
			if err != nil {
				return 0, diagnostics{}, err
			}
			oldTime := time.UnixMilli(existing.TimestampMS)
			if oldTime.Before(timestamp) {
				timestamp = oldTime
			}
		}
		encoded, err := json.Marshal(candidate.Iterations)
		if err != nil {
			return 0, diagnostics{}, err
		}
		message := store.Message{MessageID: candidate.MessageID, Score: candidate.Score, TimestampMS: timestamp.UnixMilli(), IterationsJSON: string(encoded)}
		usages, err := messageUsages(message, location)
		if err != nil {
			return 0, diagnostics{}, err
		}
		changes = append(changes, change{message: message, usages: usages, oldUses: oldUses})
		working[candidate.MessageID] = message
	}

	var applied int
	var diag diagnostics
	err = database.Transaction(func(tx *store.Tx) error {
		for _, change := range changes {
			for _, value := range change.oldUses {
				negate(&value)
				if err := tx.ApplyUsage(value, ""); err != nil {
					return err
				}
			}
			for _, value := range change.usages {
				if err := tx.ApplyUsage(value, ""); err != nil {
					return err
				}
				applied++
				addUsageDiagnostics(&diag, value)
			}
			if err := tx.PutMessage(change.message); err != nil {
				return err
			}
		}
		return tx.PutCheckpoint(checkpointFor(file, parsed.offset, parsed.hash, "", now))
	})
	if err != nil {
		return 0, diagnostics{}, err
	}
	for _, change := range changes {
		messages[change.message.MessageID] = change.message
	}
	return applied, diag, nil
}

func rebuildClaudeBuckets(database *store.Store, messages map[string]store.Message, newLocation *time.Location) error {
	return database.Transaction(func(tx *store.Tx) error {
		if err := tx.DeleteProviderUsage(discover.ProviderClaude); err != nil {
			return err
		}
		for _, message := range messages {
			newUsages, err := messageUsages(message, newLocation)
			if err != nil {
				return err
			}
			for _, value := range newUsages {
				if err := tx.ApplyUsage(value, ""); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func messageUsages(message store.Message, location *time.Location) ([]store.Usage, error) {
	var iterations []claude.Iteration
	if err := json.Unmarshal([]byte(message.IterationsJSON), &iterations); err != nil {
		return nil, fmt.Errorf("decode retained Claude message %q: %w", message.MessageID, err)
	}
	timestamp := time.UnixMilli(message.TimestampMS)
	values := make(map[usageKey]store.Usage)
	for _, item := range iterations {
		input := item.RawInput + item.CacheRead + item.CacheCreation
		if input+item.Output <= 0 {
			continue
		}
		unclassified := item.CacheCreation - item.CacheWrite5m - item.CacheWrite1h
		if unclassified < 0 {
			unclassified = 0
		}
		aggregate(values, ingest.UsageEvent{
			Timestamp: timestamp, Provider: discover.ProviderClaude, Model: item.Model,
			Input: input, CacheRead: item.CacheRead, CacheWrite5m: item.CacheWrite5m,
			CacheWrite1h: item.CacheWrite1h, CacheWriteUnclassified: unclassified, Output: item.Output,
		}, location)
	}
	result := make([]store.Usage, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result, nil
}

func checkpointFor(file discover.SourceFile, offset int64, hash, parserState string, now time.Time) store.Checkpoint {
	size := file.Size
	if offset > size {
		size = offset
	}
	return store.Checkpoint{
		Path: file.Path, Provider: file.Provider, Size: size, ModTimeUnix: file.ModTime.UnixNano(),
		ByteOffset: offset, TailHash: hash, ParserState: parserState, LastSyncedUnix: now.Unix(),
	}
}

func tailHash(path string, offset int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %q for fingerprint: %w", path, err)
	}
	defer file.Close()
	return tailHashFile(file, offset)
}

func tailHashFile(file *os.File, offset int64) (string, error) {
	start := offset - fingerprintWindow
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	data := make([]byte, offset-start)
	if _, err := io.ReadFull(file, data); err != nil {
		return "", fmt.Errorf("read fingerprint for %q: %w", file.Name(), err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func addDiagnostics(summary *Summary, values map[usageKey]store.Usage) {
	for _, value := range values {
		summary.UnclassifiedCacheWriteTokens += value.CacheWriteUnclassified
	}
}

func addUsageDiagnostics(diag *diagnostics, value store.Usage) {
	if value.Model == "unknown" {
		diag.UnknownModelTokens += value.Input + value.Output
	}
	diag.UnclassifiedCacheWriteTokens += value.CacheWriteUnclassified
}

func negate(value *store.Usage) {
	value.Input *= -1
	value.CacheRead *= -1
	value.CacheWrite5m *= -1
	value.CacheWrite1h *= -1
	value.CacheWriteUnclassified *= -1
	value.Output *= -1
	value.Reasoning *= -1
}

func negateEvent(event *ingest.UsageEvent) {
	event.Input *= -1
	event.CacheRead *= -1
	event.CacheWrite5m *= -1
	event.CacheWrite1h *= -1
	event.CacheWriteUnclassified *= -1
	event.Output *= -1
	event.Reasoning *= -1
}
