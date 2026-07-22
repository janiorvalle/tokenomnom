// Package indexer incrementally reconciles mutable provider transcript files.
package indexer

import (
	"crypto/sha256"
	"encoding"
	"encoding/binary"
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
	// SkipRunRecord lets a combined provider+vault command record one scope-wide
	// attempt after both independent source kinds finish.
	SkipRunRecord  bool
	NarrowSource   bool
	IndexAssistant bool
	// CompleteAssistantScope is true only when every configured provider and
	// vault source is included in the enclosing run.
	CompleteAssistantScope bool
}

// Issue is bounded non-content failure or warning detail.
type Issue struct {
	Provider string `json:"provider,omitempty"`
	Path     string `json:"path,omitempty"`
	Error    string `json:"error"`
}

// Summary reports aggregate work without emitting one object per source.
type Summary struct {
	ScannedSources      int                        `json:"scanned_sources"`
	IndexedSources      int                        `json:"indexed_sources"`
	NewSources          int                        `json:"new_sources"`
	SkippedSources      int                        `json:"skipped_sources"`
	AppendedSources     int                        `json:"appended_sources"`
	RewrittenSources    int                        `json:"rewritten_sources"`
	MissingSources      int                        `json:"missing_sources"`
	IndexedPrompts      int                        `json:"indexed_prompts"`
	OversizedPrompts    int                        `json:"oversized_prompts"`
	ReclassifiedPrompts int                        `json:"reclassified_prompts"`
	PromptKindCounts    map[history.PromptKind]int `json:"prompt_kind_counts"`
	ErrorCount          int                        `json:"error_count"`
	Errors              []Issue                    `json:"errors"`
	Warnings            []Issue                    `json:"warnings"`
	Full                bool                       `json:"full"`
	Duration            time.Duration              `json:"-"`
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
	summary := Summary{Errors: []Issue{}, Warnings: []Issue{}, Full: options.Full, PromptKindCounts: map[history.PromptKind]int{}}
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
	if err := options.Store.ConfigureAssistantIndexing(options.IndexAssistant); err != nil {
		return summary, err
	}
	if err := options.Store.PrepareSampling(); err != nil {
		return summary, fmt.Errorf("prepare history sampling index: %w", err)
	}

	checkpoints, err := options.Store.Checkpoints()
	if err != nil {
		return summary, err
	}
	files, discoveryFailed := discoverFiles(options.Store, options.Roots, selected, &summary)
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
		indexed, indexErr := indexFile(options.Store, file, checkpoint, found, kind, options.IndexAssistant)
		if indexErr != nil {
			recordError(options.Store, provider, file.Path, indexErr, &summary)
			continue
		}
		summary.IndexedSources++
		summary.IndexedPrompts += len(indexed.Prompts)
		for _, diagnostic := range indexed.Diagnostics {
			addIssue(&summary, Issue{
				Provider: string(provider),
				Path:     file.Path,
				Error:    fmt.Sprintf("line %d: %s", diagnostic.LineNumber, diagnostic.Message),
			}, false)
		}
		for _, prompt := range indexed.Prompts {
			kind := prompt.PromptKind
			if kind == "" {
				kind = history.ClassifyPromptKind(prompt.CleanText, prompt.Role, prompt.Classification)
			}
			summary.PromptKindCounts[kind]++
			if isReclassifiedPrompt(prompt, kind) {
				summary.ReclassifiedPrompts++
			}
			if prompt.Oversized {
				summary.OversizedPrompts++
			}
		}
		switch indexed.Kind {
		case fileNew:
			summary.NewSources++
		case fileAppend:
			summary.AppendedSources++
		case fileRewrite:
			summary.RewrittenSources++
		}
	}
	retainedErrors, err := options.Store.SourceErrors()
	if err != nil {
		return summary, err
	}
	for _, sourceError := range retainedErrors {
		key := historystore.CheckpointKey(sourceError.Provider, sourceError.Path)
		if selected[sourceError.Provider] && !seen[key] && !discoveryFailed[sourceError.Provider] {
			if err := options.Store.ClearSourceError(sourceError.Provider, sourceError.Path); err != nil {
				return summary, err
			}
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

	completeScope := selected[history.ProviderCodex] && selected[history.ProviderClaude] && !options.NarrowSource
	if !options.SkipRunRecord {
		if err := options.Store.RecordScopedRun(attempt, summary.ErrorCount, completeScope); err != nil {
			return summary, err
		}
	}
	summary.Duration = time.Since(started)
	if summary.ErrorCount > 0 {
		return summary, PartialError{Count: summary.ErrorCount}
	}
	if options.IndexAssistant && options.CompleteAssistantScope {
		completedProviders := options.Providers
		if len(completedProviders) == 0 {
			completedProviders = []history.Provider{history.ProviderCodex, history.ProviderClaude}
		}
		if err := options.Store.MarkAssistantIndexingComplete(completedProviders...); err != nil {
			return summary, err
		}
	}
	return summary, nil
}

func isReclassifiedPrompt(prompt history.Prompt, kind history.PromptKind) bool {
	if prompt.Role != history.RoleUser || prompt.Classification != history.ClassificationHuman {
		return false
	}
	switch kind {
	case history.PromptKindDelegation, history.PromptKindAgentMessage, history.PromptKindCommand, history.PromptKindControl:
		return true
	default:
		return false
	}
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
		if present[key] || discoveryFailed[checkpoint.Provider] || checkpoint.ContentSHA256 == "" || checkpoint.Size != checkpoint.CompleteOffset {
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
			if hash == checkpoint.ContentSHA256 {
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

func discoverFiles(database *historystore.Store, roots []discover.Root, selected map[history.Provider]bool, summary *Summary) ([]discover.SourceFile, map[history.Provider]bool) {
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
			recordError(database, provider, root.Path, walkErr, summary)
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
	if file.Size < checkpoint.Size || file.Size < checkpoint.CompleteOffset {
		return fileRewrite, nil
	}
	if file.Size == checkpoint.Size && file.ModTime.UnixNano() != checkpoint.ModTimeUnixNano {
		return fileRewrite, nil
	}
	prefix, err := prefixFingerprint(file.Path, checkpoint.CompleteOffset)
	if err != nil {
		return fileRewrite, err
	}
	tail, err := tailHash(file.Path, checkpoint.CompleteOffset)
	if err != nil {
		return fileRewrite, err
	}
	if file.Size == checkpoint.Size && prefix == checkpoint.PrefixFingerprint && tail == checkpoint.TailFingerprint {
		continuityHash, err := hashPathPrefix(file.Path, checkpoint.CompleteOffset)
		if err != nil {
			return fileRewrite, err
		}
		if continuityHash == checkpoint.ContentSHA256 {
			return fileUnchanged, nil
		}
		return fileRewrite, nil
	}
	if file.Size > checkpoint.Size && file.Size >= checkpoint.CompleteOffset && prefix == checkpoint.PrefixFingerprint && tail == checkpoint.TailFingerprint {
		continuityHash, err := hashPathPrefix(file.Path, checkpoint.CompleteOffset)
		if err != nil {
			return fileRewrite, err
		}
		if continuityHash == checkpoint.ContentSHA256 {
			return fileAppend, nil
		}
	}
	return fileRewrite, nil
}

type indexedFile struct {
	Prompts     []history.Prompt
	Diagnostics []history.Diagnostic
	Kind        fileKind
}

func indexFile(database *historystore.Store, file discover.SourceFile, checkpoint historystore.Checkpoint, found bool, kind fileKind, indexAssistant bool) (indexedFile, error) {
	position := jsonl.Position{}
	if kind == fileAppend {
		position = jsonl.Position{ByteOffset: checkpoint.CompleteOffset, LineNumber: checkpoint.LineCount}
	}
	source := history.SourceReference{Provider: history.Provider(file.Provider), Kind: locationKind(file.Kind), Path: file.Path}
	var parsed parsedSource
	var accumulator *extractionAccumulator
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		accumulator, err = newExtractionAccumulator(source, checkpoint, kind, indexAssistant)
		if err != nil {
			return indexedFile{}, err
		}
		parsed, err = readRecords(file.Path, position, checkpoint, kind, accumulator.visit)
		if !errors.Is(err, errSourceChanged) {
			break
		}
		kind = fileRewrite
		position = jsonl.Position{}
	}
	if err != nil {
		return indexedFile{}, err
	}
	extraction, extractorState, err := accumulator.result()
	if err != nil {
		return indexedFile{}, err
	}
	head := history.SourceHead{
		Source: source, ContentSHA256: parsed.contentHash, ContentHashState: parsed.hashState,
		PrefixFingerprint: parsed.prefixFingerprint, TailFingerprint: parsed.tailFingerprint, ExtractorState: extractorState,
		Size: parsed.size, ModTimeUnix: parsed.modTimeUnixNano, CompleteOffset: parsed.position.ByteOffset,
		LineCount: parsed.position.LineNumber, Available: true,
		VerifiedContinuity: found && kind != fileRewrite,
	}
	if kind == fileAppend && parsed.recordCount == 0 {
		if err := database.UpdateCheckpointOnly(head); err != nil {
			return indexedFile{}, err
		}
		return indexedFile{Kind: kind}, nil
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
	return indexedFile{Prompts: extraction.Prompts, Diagnostics: extraction.Diagnostics, Kind: kind}, nil
}

var errSourceChanged = errors.New("history source changed during indexing")

type parsedSource struct {
	recordCount       int
	position          jsonl.Position
	contentHash       string
	hashState         string
	prefixFingerprint string
	tailFingerprint   string
	size              int64
	modTimeUnixNano   int64
}

type hashState interface {
	io.Writer
	Sum([]byte) []byte
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
}

func readRecords(path string, position jsonl.Position, checkpoint historystore.Checkpoint, kind fileKind, visit func(jsonl.Record)) (parsedSource, error) {
	return readRecordsWithHook(path, position, checkpoint, kind, visit, nil)
}

func readRecordsWithHook(path string, position jsonl.Position, checkpoint historystore.Checkpoint, kind fileKind, visit func(jsonl.Record), afterRead func()) (parsedSource, error) {
	file, err := os.Open(path)
	if err != nil {
		return parsedSource{}, fmt.Errorf("open history source %q: %w", path, err)
	}
	defer file.Close()
	initialStat, err := file.Stat()
	if err != nil {
		return parsedSource{}, fmt.Errorf("stat initial history source %q: %w", path, err)
	}
	if position.ByteOffset > initialStat.Size() {
		return parsedSource{}, fmt.Errorf("%w: %s is shorter than its checkpoint", errSourceChanged, path)
	}
	snapshotBefore, err := prefixFingerprintFile(file, initialStat.Size())
	if err != nil {
		return parsedSource{}, err
	}
	hasher, ok := sha256.New().(hashState)
	if !ok {
		return parsedSource{}, errors.New("SHA-256 implementation does not support resumable state")
	}
	if kind == fileAppend {
		continuityHash, err := hashFilePrefix(file, position.ByteOffset)
		if err != nil {
			return parsedSource{}, fmt.Errorf("verify append prefix for %q: %w", path, err)
		}
		if continuityHash != checkpoint.ContentSHA256 {
			return parsedSource{}, fmt.Errorf("%w: checkpoint prefix of %s changed", errSourceChanged, path)
		}
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
	recordCount := 0
	finalPosition, err := jsonl.ReadPositionedFileLimit(file, position, initialStat.Size(), func(record jsonl.Record) {
		recordCount++
		_, _ = hasher.Write(record.Raw)
		if visit != nil {
			visit(record)
		}
	})
	if err != nil {
		return parsedSource{}, err
	}
	if afterRead != nil {
		afterRead()
	}
	stat, err := file.Stat()
	if err != nil {
		return parsedSource{}, fmt.Errorf("stat history source %q: %w", path, err)
	}
	if stat.Size() < initialStat.Size() {
		return parsedSource{}, fmt.Errorf("%w: %s shrank while being read", errSourceChanged, path)
	}
	if stat.Size() == initialStat.Size() && stat.ModTime().UnixNano() != initialStat.ModTime().UnixNano() {
		return parsedSource{}, fmt.Errorf("%w: %s was rewritten while being read", errSourceChanged, path)
	}
	snapshotAfter, err := prefixFingerprintFile(file, initialStat.Size())
	if err != nil {
		return parsedSource{}, fmt.Errorf("%w: verify snapshot of %s: %v", errSourceChanged, path, err)
	}
	if snapshotBefore != snapshotAfter {
		return parsedSource{}, fmt.Errorf("%w: bounded fingerprint of %s changed", errSourceChanged, path)
	}
	contentHash := hex.EncodeToString(hasher.Sum(nil))
	if stat.Size() > initialStat.Size() {
		verifiedHash, err := hashFilePrefix(file, finalPosition.ByteOffset)
		if err != nil {
			return parsedSource{}, fmt.Errorf("%w: verify indexed prefix of %s: %v", errSourceChanged, path, err)
		}
		if verifiedHash != contentHash {
			return parsedSource{}, fmt.Errorf("%w: indexed prefix of %s changed", errSourceChanged, path)
		}
	}
	prefixFingerprint, err := prefixFingerprintFile(file, finalPosition.ByteOffset)
	if err != nil {
		return parsedSource{}, err
	}
	tailFingerprint, err := tailHashFile(file, finalPosition.ByteOffset)
	if err != nil {
		return parsedSource{}, err
	}
	state, err := hasher.MarshalBinary()
	if err != nil {
		return parsedSource{}, fmt.Errorf("encode content hash state for %q: %w", path, err)
	}
	return parsedSource{
		recordCount: recordCount, position: finalPosition, contentHash: contentHash, hashState: hex.EncodeToString(state),
		prefixFingerprint: prefixFingerprint, tailFingerprint: tailFingerprint,
		size: initialStat.Size(), modTimeUnixNano: initialStat.ModTime().UnixNano(),
	}, nil
}

type extractionAccumulator struct {
	source      history.SourceReference
	extraction  history.Extraction
	promptIndex map[string]int
	codexState  codex.State
	claudeState claude.State
	options     history.ExtractionOptions
}

func newExtractionAccumulator(source history.SourceReference, checkpoint historystore.Checkpoint, kind fileKind, indexAssistant bool) (*extractionAccumulator, error) {
	value := &extractionAccumulator{
		source:      source,
		extraction:  history.Extraction{Provider: source.Provider, Source: source},
		promptIndex: make(map[string]int),
		options:     history.ExtractionOptions{IndexAssistant: indexAssistant},
	}
	if kind == fileAppend {
		switch source.Provider {
		case history.ProviderCodex:
			value.codexState.Session = checkpoint.Session
			if checkpoint.ExtractorState != "" {
				if err := json.Unmarshal([]byte(checkpoint.ExtractorState), &value.codexState); err != nil {
					return nil, fmt.Errorf("decode Codex history extractor state for %q: %w", source.Path, err)
				}
			}
		case history.ProviderClaude:
			value.claudeState.Session = checkpoint.Session
			if checkpoint.ExtractorState != "" {
				if err := json.Unmarshal([]byte(checkpoint.ExtractorState), &value.claudeState); err != nil {
					return nil, fmt.Errorf("decode Claude history extractor state for %q: %w", source.Path, err)
				}
			}
		}
	}
	if err := value.consume(nil); err != nil {
		return nil, err
	}
	return value, nil
}

func (a *extractionAccumulator) visit(record jsonl.Record) {
	_ = a.consume([]jsonl.Record{record})
}

func (a *extractionAccumulator) consume(records []jsonl.Record) error {
	var part history.Extraction
	switch a.source.Provider {
	case history.ProviderCodex:
		part, a.codexState = codex.ExtractWithStateOptions(a.source, records, a.codexState, a.options)
	case history.ProviderClaude:
		part, a.claudeState = claude.ExtractWithStateOptions(a.source, records, a.claudeState, a.options)
	default:
		return fmt.Errorf("unsupported history provider %q", a.source.Provider)
	}
	a.extraction.Session = part.Session
	a.extraction.Relationships = part.Relationships
	for _, prompt := range part.Prompts {
		if index, found := a.promptIndex[prompt.LogicalKey]; found {
			if history.CanonicalPromptWins(prompt, a.extraction.Prompts[index]) {
				a.extraction.Prompts[index] = prompt
			}
			continue
		}
		a.promptIndex[prompt.LogicalKey] = len(a.extraction.Prompts)
		a.extraction.Prompts = append(a.extraction.Prompts, prompt)
	}
	a.extraction.Occurrences = append(a.extraction.Occurrences, part.Occurrences...)
	remainingDiagnostics := maxIssues - len(a.extraction.Diagnostics)
	if remainingDiagnostics > len(part.Diagnostics) {
		remainingDiagnostics = len(part.Diagnostics)
	}
	if remainingDiagnostics > 0 {
		a.extraction.Diagnostics = append(a.extraction.Diagnostics, part.Diagnostics[:remainingDiagnostics]...)
	}
	return nil
}

func (a *extractionAccumulator) result() (history.Extraction, string, error) {
	var state any
	if a.source.Provider == history.ProviderCodex {
		state = a.codexState
	} else {
		state = a.claudeState
	}
	encoded, err := json.Marshal(state)
	return a.extraction, string(encoded), err
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

func hashPathPrefix(path string, offset int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %q for exact prefix fingerprint: %w", path, err)
	}
	defer file.Close()
	hash, err := hashFilePrefix(file, offset)
	if err != nil {
		return "", fmt.Errorf("hash exact prefix fingerprint for %q: %w", path, err)
	}
	return hash, nil
}

func hashFilePrefix(file *os.File, offset int64) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hasher := sha256.New()
	if _, err := io.CopyN(hasher, file, offset); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func prefixFingerprint(path string, size int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %q for bounded prefix fingerprint: %w", path, err)
	}
	defer file.Close()
	return prefixFingerprintFile(file, size)
}

func prefixFingerprintFile(file *os.File, size int64) (string, error) {
	if size < 0 {
		return "", fmt.Errorf("invalid prefix fingerprint size %d for %q", size, file.Name())
	}
	hasher := sha256.New()
	var encoded [8]byte
	binary.LittleEndian.PutUint64(encoded[:], uint64(size))
	_, _ = hasher.Write(encoded[:])
	window := fingerprintWindow
	if size < window {
		window = size
	}
	offsets := []int64{0, size / 4, size / 2, (size * 3) / 4, size - window}
	seen := make(map[int64]bool, len(offsets))
	for _, offset := range offsets {
		if offset+window > size {
			offset = size - window
		}
		if offset < 0 {
			offset = 0
		}
		if seen[offset] {
			continue
		}
		seen[offset] = true
		binary.LittleEndian.PutUint64(encoded[:], uint64(offset))
		_, _ = hasher.Write(encoded[:])
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return "", fmt.Errorf("seek bounded prefix fingerprint for %q: %w", file.Name(), err)
		}
		if _, err := io.CopyN(hasher, file, window); err != nil {
			return "", fmt.Errorf("read bounded prefix fingerprint for %q: %w", file.Name(), err)
		}
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
