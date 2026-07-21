// Package indexer incrementally reconciles mutable provider transcript files.
package indexer

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/history/claude"
	"github.com/janiorvalle/tokenomnom/internal/history/codex"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/ingest/jsonl"
)

const (
	fingerprintWindow = int64(4096)
	maxIssues         = 100
)

// Options configures one explicit provider indexing pass.
type Options struct {
	Store     *historystore.Store
	Roots     []discover.Root
	Providers []history.Provider
	Full      bool
	Now       func() time.Time
	LockHeld  bool
}

// Issue is bounded non-content failure or warning detail.
type Issue struct {
	Provider string `json:"provider,omitempty"`
	Path     string `json:"path,omitempty"`
	Error    string `json:"error"`
}

// Summary reports aggregate work without emitting one object per source.
type Summary struct {
	ScannedSources   int           `json:"scanned_sources"`
	IndexedSources   int           `json:"indexed_sources"`
	NewSources       int           `json:"new_sources"`
	SkippedSources   int           `json:"skipped_sources"`
	AppendedSources  int           `json:"appended_sources"`
	RewrittenSources int           `json:"rewritten_sources"`
	MissingSources   int           `json:"missing_sources"`
	IndexedPrompts   int           `json:"indexed_prompts"`
	OversizedPrompts int           `json:"oversized_prompts"`
	ErrorCount       int           `json:"error_count"`
	Errors           []Issue       `json:"errors"`
	Warnings         []Issue       `json:"warnings"`
	Full             bool          `json:"full"`
	Duration         time.Duration `json:"-"`
}

// PartialError means independent sources failed after successful sources were
// committed. Summary remains the authoritative bounded diagnostic result.
type PartialError struct{ Count int }

func (e PartialError) Error() string {
	return fmt.Sprintf("history index completed with %d error(s)", e.Count)
}

type fileKind int

const (
	fileNew fileKind = iota
	fileUnchanged
	fileAppend
	fileRewrite
)

// Index discovers and reconciles selected provider sources.
func Index(options Options) (Summary, error) {
	started := time.Now()
	summary := Summary{Errors: []Issue{}, Warnings: []Issue{}, Full: options.Full}
	if options.Store == nil {
		return summary, errors.New("history store is required")
	}
	release := func() {}
	if !options.LockHeld {
		var err error
		release, err = historystore.Lock(options.Store.Path())
		if err != nil {
			return summary, err
		}
	}
	defer release()
	now := options.Now
	if now == nil {
		now = time.Now
	}
	attempt := now()
	selected := selectedProviders(options.Providers)

	checkpoints, err := options.Store.Checkpoints()
	if err != nil {
		return summary, err
	}
	files, discoveryFailed := discoverFiles(options.Roots, selected, &summary)
	summary.ScannedSources = len(files)
	if err := reconcileMoves(options.Store, files, checkpoints, discoveryFailed); err != nil {
		return summary, err
	}
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		provider := history.Provider(file.Provider)
		seen[historystore.CheckpointKey(provider, file.Path)] = true
		checkpoint, found := checkpoints[historystore.CheckpointKey(provider, file.Path)]
		kind, classifyErr := classify(file, checkpoint, found, options.Full)
		if classifyErr != nil {
			recordError(options.Store, provider, file.Path, classifyErr, &summary)
			continue
		}
		if kind == fileUnchanged {
			if err := options.Store.RecordSourceChecked(provider, file.Path); err != nil {
				recordError(options.Store, provider, file.Path, err, &summary)
				continue
			}
			summary.SkippedSources++
			continue
		}
		indexed, indexErr := indexFile(options.Store, file, checkpoint, found, kind)
		if indexErr != nil {
			recordError(options.Store, provider, file.Path, indexErr, &summary)
			continue
		}
		summary.IndexedSources++
		summary.IndexedPrompts += len(indexed.Prompts)
		for _, prompt := range indexed.Prompts {
			if prompt.Oversized {
				summary.OversizedPrompts++
			}
		}
		switch kind {
		case fileNew:
			summary.NewSources++
		case fileAppend:
			summary.AppendedSources++
		case fileRewrite:
			summary.RewrittenSources++
		}
	}

	for key, checkpoint := range checkpoints {
		if !selected[checkpoint.Provider] || seen[key] || discoveryFailed[checkpoint.Provider] {
			continue
		}
		changed, missingErr := options.Store.MarkSourceMissing(checkpoint.Provider, checkpoint.Path)
		if missingErr != nil {
			recordError(options.Store, checkpoint.Provider, checkpoint.Path, missingErr, &summary)
			continue
		}
		if changed {
			summary.MissingSources++
		}
	}

	if err := options.Store.RecordRun(attempt, summary.ErrorCount); err != nil {
		return summary, err
	}
	summary.Duration = time.Since(started)
	if summary.ErrorCount > 0 {
		return summary, PartialError{Count: summary.ErrorCount}
	}
	return summary, nil
}

func reconcileMoves(database *historystore.Store, files []discover.SourceFile, checkpoints map[string]historystore.Checkpoint, discoveryFailed map[history.Provider]bool) error {
	type candidateKey struct {
		provider history.Provider
		size     int64
	}
	present := make(map[string]bool, len(files))
	for _, file := range files {
		present[historystore.CheckpointKey(history.Provider(file.Provider), file.Path)] = true
	}
	missingBySize := make(map[candidateKey][]string)
	for key, checkpoint := range checkpoints {
		if present[key] || discoveryFailed[checkpoint.Provider] || checkpoint.PrefixFingerprint == "" {
			continue
		}
		candidate := candidateKey{provider: checkpoint.Provider, size: checkpoint.Size}
		missingBySize[candidate] = append(missingBySize[candidate], key)
	}
	claimed := make(map[string]bool)
	hashes := make(map[string]string)
filesLoop:
	for _, file := range files {
		provider := history.Provider(file.Provider)
		newKey := historystore.CheckpointKey(provider, file.Path)
		if _, found := checkpoints[newKey]; found || discoveryFailed[provider] {
			continue
		}
		var candidates []string
		for _, oldKey := range missingBySize[candidateKey{provider: provider, size: file.Size}] {
			if claimed[oldKey] {
				continue
			}
			checkpoint := checkpoints[oldKey]
			hash, ok := hashes[file.Path]
			if !ok {
				computed, err := fullHash(file.Path)
				if err != nil {
					continue filesLoop
				}
				hash = computed
				hashes[file.Path] = computed
			}
			if hash == checkpoint.PrefixFingerprint {
				candidates = append(candidates, oldKey)
			}
		}
		if len(candidates) != 1 {
			continue
		}
		oldKey := candidates[0]
		checkpoint := checkpoints[oldKey]
		source := history.SourceReference{Provider: provider, Kind: locationKind(file.Kind), Path: file.Path}
		if err := database.RelocateSource(provider, checkpoint.Path, source); err != nil {
			return fmt.Errorf("relocate history source %q to %q: %w", checkpoint.Path, file.Path, err)
		}
		delete(checkpoints, oldKey)
		checkpoint.Path = file.Path
		checkpoint.Kind = source.Kind
		checkpoint.SourceKind = sourceKind(file.Kind)
		checkpoints[newKey] = checkpoint
		claimed[oldKey] = true
	}
	return nil
}

func selectedProviders(values []history.Provider) map[history.Provider]bool {
	if len(values) == 0 {
		return map[history.Provider]bool{history.ProviderCodex: true, history.ProviderClaude: true}
	}
	result := make(map[history.Provider]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func discoverFiles(roots []discover.Root, selected map[history.Provider]bool, summary *Summary) ([]discover.SourceFile, map[history.Provider]bool) {
	var files []discover.SourceFile
	failed := make(map[history.Provider]bool)
	for _, root := range roots {
		provider := history.Provider(root.Provider)
		if !selected[provider] {
			continue
		}
		found, walkErrors := discover.ListSourceFiles(root)
		files = append(files, found...)
		for _, walkErr := range walkErrors {
			failed[provider] = true
			addIssue(summary, Issue{Provider: string(provider), Path: root.Path, Error: walkErr.Error()}, true)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Provider != files[j].Provider {
			return files[i].Provider < files[j].Provider
		}
		return files[i].Path < files[j].Path
	})
	return files, failed
}

func classify(file discover.SourceFile, checkpoint historystore.Checkpoint, found, full bool) (fileKind, error) {
	if !found {
		return fileNew, nil
	}
	if full || checkpoint.Missing || checkpoint.ExtractorVersion != history.ExtractorVersion || checkpoint.Kind != locationKind(file.Kind) {
		return fileRewrite, nil
	}
	fingerprint, err := fullHash(file.Path)
	if err != nil {
		return fileRewrite, err
	}
	if file.Size == checkpoint.Size && fingerprint == checkpoint.PrefixFingerprint {
		return fileUnchanged, nil
	}
	if file.Size >= checkpoint.CompleteOffset {
		tail, err := tailHash(file.Path, checkpoint.CompleteOffset)
		if err != nil {
			return fileRewrite, err
		}
		prefix, err := prefixHash(file.Path, checkpoint.CompleteOffset)
		if err != nil {
			return fileRewrite, err
		}
		if tail == checkpoint.TailFingerprint && prefix == checkpoint.ContentSHA256 {
			return fileAppend, nil
		}
	}
	return fileRewrite, nil
}

type indexedFile struct {
	Prompts []history.Prompt
}

func indexFile(database *historystore.Store, file discover.SourceFile, checkpoint historystore.Checkpoint, found bool, kind fileKind) (indexedFile, error) {
	position := jsonl.Position{}
	if kind == fileAppend {
		position = jsonl.Position{ByteOffset: checkpoint.CompleteOffset, LineNumber: checkpoint.LineCount}
	}
	var parsed parsedSource
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		parsed, err = readRecords(file.Path, position, checkpoint, kind)
		if !errors.Is(err, errSourceChanged) {
			break
		}
		kind = fileRewrite
		position = jsonl.Position{}
	}
	if err != nil {
		return indexedFile{}, err
	}
	source := history.SourceReference{Provider: history.Provider(file.Provider), Kind: locationKind(file.Kind), Path: file.Path}
	extraction, extractorState, err := extract(source, parsed.records, checkpoint, kind)
	if err != nil {
		return indexedFile{}, err
	}
	head := history.SourceHead{
		Source: source, ContentSHA256: parsed.contentHash, ContentHashState: parsed.hashState,
		PrefixFingerprint: parsed.physicalFingerprint, TailFingerprint: parsed.tailFingerprint, ExtractorState: extractorState,
		Size: parsed.size, ModTimeUnix: parsed.modTimeUnixNano, CompleteOffset: parsed.position.ByteOffset,
		LineCount: parsed.position.LineNumber, Available: true,
		VerifiedContinuity: found && kind != fileRewrite,
	}
	if kind == fileAppend && len(parsed.records) == 0 {
		if err := database.UpdateCheckpointOnly(head); err != nil {
			return indexedFile{}, err
		}
		return indexedFile{}, nil
	}
	mode := historystore.ApplyReplace
	if kind == fileAppend {
		mode = historystore.ApplyAppend
	}
	advanceGeneration := true
	if kind == fileRewrite && found && !checkpoint.Missing && checkpoint.Kind == source.Kind && checkpoint.ContentSHA256 == parsed.contentHash && checkpoint.ExtractorVersion == history.ExtractorVersion {
		advanceGeneration = false
	}
	if _, err := database.ApplySourceWithGeneration(extraction, head, mode, advanceGeneration); err != nil {
		return indexedFile{}, err
	}
	return indexedFile{Prompts: extraction.Prompts}, nil
}

var errSourceChanged = errors.New("history source changed during indexing")

type parsedSource struct {
	records             []jsonl.Record
	position            jsonl.Position
	contentHash         string
	hashState           string
	physicalFingerprint string
	tailFingerprint     string
	size                int64
	modTimeUnixNano     int64
}

type hashState interface {
	io.Writer
	Sum([]byte) []byte
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
}

func readRecords(path string, position jsonl.Position, checkpoint historystore.Checkpoint, kind fileKind) (parsedSource, error) {
	return readRecordsWithHook(path, position, checkpoint, kind, nil)
}

func readRecordsWithHook(path string, position jsonl.Position, checkpoint historystore.Checkpoint, kind fileKind, afterRead func()) (parsedSource, error) {
	file, err := os.Open(path)
	if err != nil {
		return parsedSource{}, fmt.Errorf("open history source %q: %w", path, err)
	}
	defer file.Close()
	hasher, ok := sha256.New().(hashState)
	if !ok {
		return parsedSource{}, errors.New("SHA-256 implementation does not support resumable state")
	}
	if kind == fileAppend && checkpoint.ContentHashState != "" {
		encoded, err := hex.DecodeString(checkpoint.ContentHashState)
		if err != nil {
			return parsedSource{}, fmt.Errorf("decode content hash state for %q: %w", path, err)
		}
		if err := hasher.UnmarshalBinary(encoded); err != nil {
			return parsedSource{}, fmt.Errorf("restore content hash state for %q: %w", path, err)
		}
	} else if position.ByteOffset > 0 {
		if _, err := io.CopyN(hasher, file, position.ByteOffset); err != nil {
			return parsedSource{}, fmt.Errorf("hash existing content prefix for %q: %w", path, err)
		}
	}
	var records []jsonl.Record
	finalPosition, err := jsonl.ReadPositionedFile(file, position, func(record jsonl.Record) {
		record.Raw = append([]byte(nil), record.Raw...)
		records = append(records, record)
		_, _ = hasher.Write(record.Raw)
	})
	if err != nil {
		return parsedSource{}, err
	}
	if afterRead != nil {
		afterRead()
	}
	observedEOF, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return parsedSource{}, fmt.Errorf("read observed EOF for history source %q: %w", path, err)
	}
	stat, err := file.Stat()
	if err != nil {
		return parsedSource{}, fmt.Errorf("stat history source %q: %w", path, err)
	}
	if stat.Size() < observedEOF {
		return parsedSource{}, fmt.Errorf("%w: %s shrank while being read", errSourceChanged, path)
	}
	contentHash := hex.EncodeToString(hasher.Sum(nil))
	physicalFingerprint, err := prefixHashFile(file, observedEOF)
	if err != nil {
		return parsedSource{}, err
	}
	verifiedContentHash, err := prefixHashFile(file, finalPosition.ByteOffset)
	if err != nil {
		return parsedSource{}, err
	}
	if verifiedContentHash != contentHash {
		return parsedSource{}, fmt.Errorf("%w: complete prefix of %s was rewritten", errSourceChanged, path)
	}
	tailFingerprint, err := tailHashFile(file, finalPosition.ByteOffset)
	if err != nil {
		return parsedSource{}, err
	}
	state, err := hasher.MarshalBinary()
	if err != nil {
		return parsedSource{}, fmt.Errorf("encode content hash state for %q: %w", path, err)
	}
	modTimeUnixNano := stat.ModTime().UnixNano()
	if stat.Size() != observedEOF {
		modTimeUnixNano = 0
	}
	return parsedSource{
		records: records, position: finalPosition, contentHash: contentHash, hashState: hex.EncodeToString(state),
		physicalFingerprint: physicalFingerprint, tailFingerprint: tailFingerprint,
		size: observedEOF, modTimeUnixNano: modTimeUnixNano,
	}, nil
}

func extract(source history.SourceReference, records []jsonl.Record, checkpoint historystore.Checkpoint, kind fileKind) (history.Extraction, string, error) {
	switch source.Provider {
	case history.ProviderCodex:
		state := codex.State{}
		if kind == fileAppend {
			state.Session = checkpoint.Session
			if checkpoint.ExtractorState != "" {
				if err := json.Unmarshal([]byte(checkpoint.ExtractorState), &state); err != nil {
					return history.Extraction{}, "", fmt.Errorf("decode Codex history extractor state for %q: %w", source.Path, err)
				}
			}
		}
		extraction, state := codex.ExtractWithState(source, records, state)
		encoded, err := json.Marshal(state)
		return extraction, string(encoded), err
	case history.ProviderClaude:
		prior := history.Session{}
		if kind == fileAppend {
			prior = checkpoint.Session
			if checkpoint.ExtractorState != "" {
				if err := json.Unmarshal([]byte(checkpoint.ExtractorState), &prior); err != nil {
					return history.Extraction{}, "", fmt.Errorf("decode Claude history extractor state for %q: %w", source.Path, err)
				}
			}
		}
		extraction := claude.ExtractWithSession(source, records, prior)
		encoded, err := json.Marshal(extraction.Session)
		return extraction, string(encoded), err
	default:
		return history.Extraction{}, "", fmt.Errorf("unsupported history provider %q", source.Provider)
	}
}

func locationKind(kind discover.SourceKind) history.LocationKind {
	if kind == discover.SourceCodexArchive {
		return history.LocationProviderArchive
	}
	return history.LocationProviderLive
}

func sourceKind(kind discover.SourceKind) string {
	switch kind {
	case discover.SourceCodexArchive:
		return "codex_archive"
	case discover.SourceClaudeProject:
		return "claude_project"
	default:
		return "codex_live"
	}
}

func tailHash(path string, offset int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %q for history fingerprint: %w", path, err)
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
	hasher := sha256.New()
	if _, err := io.CopyN(hasher, file, offset-start); err != nil {
		return "", fmt.Errorf("read history fingerprint for %q: %w", file.Name(), err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func fullHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %q for exact fingerprint: %w", path, err)
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("hash exact fingerprint for %q: %w", path, err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func prefixHash(path string, size int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %q for exact prefix hash: %w", path, err)
	}
	defer file.Close()
	return prefixHashFile(file, size)
}

func prefixHashFile(file *os.File, size int64) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek exact prefix for %q: %w", file.Name(), err)
	}
	hasher := sha256.New()
	if _, err := io.CopyN(hasher, file, size); err != nil {
		return "", fmt.Errorf("hash exact prefix for %q: %w", file.Name(), err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func recordError(database *historystore.Store, provider history.Provider, path string, err error, summary *Summary) {
	_ = database.RecordSourceError(provider, path, err)
	addIssue(summary, Issue{Provider: string(provider), Path: path, Error: err.Error()}, true)
}

func addIssue(summary *Summary, issue Issue, indexError bool) {
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
