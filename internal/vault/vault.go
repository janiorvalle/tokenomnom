// Package vault stores lossless transcript archives and their manifest.
package vault

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

const (
	FormatVersion            = 1
	markerName               = "vault.json"
	brokenArchivesMeta       = "vault_broken_archives"
	lastArchiveMeta          = "last_vault_archive_unix"
	lastDeepVerificationMeta = "last_vault_deep_verify_unix"
	lastReclaimableBytesMeta = "last_vault_reclaimable_bytes"
	lastStatusScanMeta       = "last_vault_status_unix"
)

type Marker struct {
	VaultFormat int    `json:"vault_format"`
	Encryption  string `json:"encryption"`
}

type Options struct {
	Dir        string
	Store      *store.Store
	Roots      []discover.Root
	Providers  []discover.Provider
	MinAge     time.Duration
	Now        func() time.Time
	BeforeRead func(string)
}

type Vault struct {
	dir        string
	store      *store.Store
	roots      []discover.Root
	providers  map[discover.Provider]bool
	minAge     time.Duration
	now        func() time.Time
	beforeRead func(string)
}

func New(options Options) (*Vault, error) {
	if options.Dir == "" {
		return nil, errors.New("vault directory is required")
	}
	if options.Store == nil {
		return nil, errors.New("vault manifest store is required")
	}
	providers := make(map[discover.Provider]bool, len(options.Providers))
	for _, provider := range options.Providers {
		if provider != discover.ProviderCodex && provider != discover.ProviderClaude {
			return nil, fmt.Errorf("unknown vault provider %q", provider)
		}
		providers[provider] = true
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	absDir, err := filepath.Abs(options.Dir)
	if err != nil {
		return nil, fmt.Errorf("resolve vault directory: %w", err)
	}
	return &Vault{dir: absDir, store: options.Store, roots: options.Roots, providers: providers, minAge: options.MinAge, now: now, beforeRead: options.BeforeRead}, nil
}

func (v *Vault) Dir() string { return v.dir }

func (v *Vault) EnsureFormat() (Marker, error) {
	if err := os.MkdirAll(v.dir, 0o700); err != nil {
		return Marker{}, fmt.Errorf("create vault directory: %w", err)
	}
	path := filepath.Join(v.dir, markerName)
	marker, err := readMarker(path)
	if err == nil {
		return marker, validateMarker(marker)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return Marker{}, err
	}
	marker = Marker{VaultFormat: FormatVersion, Encryption: "none"}
	data, _ := json.Marshal(marker)
	data = append(data, '\n')
	if err := atomicWrite(path, data, 0o600); err != nil {
		return Marker{}, fmt.Errorf("create vault marker: %w", err)
	}
	return marker, nil
}

func InspectFormat(dir string) (Marker, bool, error) {
	marker, err := readMarker(filepath.Join(dir, markerName))
	if errors.Is(err, fs.ErrNotExist) {
		return Marker{}, false, nil
	}
	if err != nil {
		return Marker{}, false, err
	}
	if err := validateMarker(marker); err != nil {
		return marker, true, err
	}
	return marker, true, nil
}

func readMarker(path string) (Marker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Marker{}, err
	}
	var marker Marker
	if err := json.Unmarshal(data, &marker); err != nil {
		return Marker{}, fmt.Errorf("read vault marker %s: %w", path, err)
	}
	return marker, nil
}

func validateMarker(marker Marker) error {
	if marker.VaultFormat != FormatVersion {
		return fmt.Errorf("unsupported vault format %d (expected %d)", marker.VaultFormat, FormatVersion)
	}
	if marker.Encryption != "none" {
		return fmt.Errorf("unsupported vault encryption %q", marker.Encryption)
	}
	return nil
}

func atomicWrite(path string, data []byte, mode fs.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".tokenomnom-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceFile(tempPath, path)
}

type ArchiveProviderResult struct {
	Provider     discover.Provider `json:"provider"`
	Archived     int               `json:"archived"`
	InputBytes   int64             `json:"input_bytes"`
	StoredBytes  int64             `json:"stored_bytes"`
	Deduplicated int               `json:"deduplicated"`
	Skipped      int               `json:"skipped"`
	Changed      int               `json:"changed_during_read"`
}

type ArchiveResult struct {
	Providers []ArchiveProviderResult `json:"providers"`
	Warnings  []string                `json:"warnings"`
}

type stagedFile struct {
	manifest store.VaultFile
	stage    string
	record   bool
}

type archiveCandidate struct {
	source discover.SourceFile
	rel    string
	latest store.VaultFile
	found  bool
	repair bool
}

func (v *Vault) Archive(all bool) (ArchiveResult, error) {
	if _, err := v.EnsureFormat(); err != nil {
		return ArchiveResult{}, err
	}
	release, err := store.LockPath(filepath.Join(v.dir, ".tokenomnom-vault.lock"))
	if err != nil {
		return ArchiveResult{}, err
	}
	defer release()

	result := ArchiveResult{}
	if err := v.recoverRollbackFiles(); err != nil {
		return result, err
	}
	if err := v.cleanupAbandonedStaging(); err != nil {
		return result, err
	}
	stagedPaths := make(map[string]bool)
	defer func() {
		for path := range stagedPaths {
			_ = os.Remove(path)
		}
	}()
	manifestFiles, err := v.store.VaultFiles()
	if err != nil {
		return result, err
	}
	brokenArchives, err := v.loadBrokenArchives()
	if err != nil {
		return result, err
	}
	manifestByArchive := make(map[string][]store.VaultFile)
	for _, manifest := range manifestFiles {
		manifestByArchive[manifest.Archive] = append(manifestByArchive[manifest.Archive], manifest)
	}
	groups := make(map[string][]archiveCandidate)
	results := make(map[discover.Provider]*ArchiveProviderResult)
	for _, root := range v.roots {
		if !v.providers[root.Provider] {
			continue
		}
		providerResult := &ArchiveProviderResult{Provider: root.Provider}
		results[root.Provider] = providerResult
		files, walkErrors := discover.ListSourceFiles(root)
		for _, walkErr := range walkErrors {
			result.Warnings = append(result.Warnings, walkErr.Error())
		}
		resolvedRoot, err := filepath.EvalSymlinks(root.Path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				result.Warnings = append(result.Warnings, err.Error())
			}
			continue
		}
		for _, source := range files {
			current, err := os.Stat(source.Path)
			if err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", source.Path, err))
				providerResult.Skipped++
				continue
			}
			source.Size, source.ModTime = current.Size(), current.ModTime()
			if !all && v.now().Sub(source.ModTime) < v.minAge {
				providerResult.Skipped++
				continue
			}
			latest, found, err := v.store.LatestVaultFile(source.Path)
			if err != nil {
				return result, err
			}
			repair := false
			if found {
				_, archiveErr := os.Stat(filepath.Join(v.dir, filepath.FromSlash(latest.Archive)))
				if errors.Is(archiveErr, fs.ErrNotExist) {
					brokenArchives[latest.Archive] = true
				}
				repair = brokenArchives[latest.Archive]
				if archiveErr != nil && !errors.Is(archiveErr, fs.ErrNotExist) {
					return result, archiveErr
				}
			}
			if found && !all && !repair && latest.Size == source.Size && latest.ModTimeUnix == source.ModTime.UnixNano() {
				providerResult.Skipped++
				continue
			}
			rel, err := relativeMember(resolvedRoot, source.Path)
			if err != nil {
				result.Warnings = append(result.Warnings, err.Error())
				providerResult.Skipped++
				continue
			}
			archive := filepath.ToSlash(filepath.Join(string(source.Provider), source.ModTime.Format("2006-01")+".tar.zst"))
			if repair {
				archive = latest.Archive
			}
			groups[archive] = append(groups[archive], archiveCandidate{source: source, rel: rel, latest: latest, found: found, repair: repair})
		}
	}

	archives := make([]string, 0, len(groups))
	rerouted := make(map[string][]stagedFile)
	for archive := range groups {
		archives = append(archives, archive)
	}
	sort.Strings(archives)
	for _, archive := range archives {
		candidates := groups[archive]
		staged := make([]stagedFile, 0, len(candidates))
		for _, candidate := range candidates {
			providerResult := results[candidate.source.Provider]
			item, changed, err := v.stage(candidate.source, candidate.rel, candidate.latest, candidate.found, candidate.repair)
			if err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", candidate.source.Path, err))
				providerResult.Skipped++
				continue
			}
			if changed {
				providerResult.Changed++
				providerResult.Skipped++
				continue
			}
			if item.stage == "" {
				providerResult.Deduplicated++
				continue
			}
			stagedPaths[item.stage] = true
			if item.manifest.Archive != archive {
				rerouted[item.manifest.Archive] = append(rerouted[item.manifest.Archive], item)
				continue
			}
			staged = append(staged, item)
		}
		if len(staged) == 0 {
			continue
		}
		rebuild := brokenArchives[archive] && stagedCoversManifest(staged, manifestByArchive[archive])
		if _, statErr := os.Stat(filepath.Join(v.dir, filepath.FromSlash(archive))); errors.Is(statErr, fs.ErrNotExist) && brokenArchives[archive] && !rebuild {
			for _, item := range staged {
				_ = os.Remove(item.stage)
			}
			return result, fmt.Errorf("cannot repair missing vault bundle %s because not every archived version is recoverable from current sources", archive)
		}
		stored, finalize, rollback, err := v.rewriteArchive(archive, staged, rebuild)
		for _, item := range staged {
			_ = os.Remove(item.stage)
		}
		if err != nil {
			return result, err
		}
		if err := v.store.Transaction(func(tx *store.Tx) error {
			for _, item := range staged {
				if item.record {
					if err := tx.PutVaultFile(item.manifest); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			if rollbackErr := rollback(); rollbackErr != nil {
				return result, errors.Join(fmt.Errorf("record vault manifest: %w", err), fmt.Errorf("restore previous vault bundle: %w", rollbackErr))
			}
			return result, fmt.Errorf("record vault manifest: %w", err)
		}
		if err := finalize(); err != nil {
			return result, fmt.Errorf("remove vault rollback link: %w", err)
		}
		if brokenArchives[archive] {
			expected := append([]store.VaultFile(nil), manifestByArchive[archive]...)
			for _, item := range staged {
				if item.record {
					expected = append(expected, item.manifest)
				}
			}
			_, failures := v.verifyFiles(expected, true)
			if len(failures) == 0 {
				delete(brokenArchives, archive)
			}
		}
		for _, item := range staged {
			providerResult := results[item.manifest.Provider]
			providerResult.Archived++
			providerResult.InputBytes += item.manifest.Size
		}
		results[staged[0].manifest.Provider].StoredBytes += stored
	}
	reroutedArchives := make([]string, 0, len(rerouted))
	for archive := range rerouted {
		reroutedArchives = append(reroutedArchives, archive)
	}
	sort.Strings(reroutedArchives)
	for _, archive := range reroutedArchives {
		staged := rerouted[archive]
		stored, finalize, rollback, err := v.rewriteArchive(archive, staged, false)
		for _, item := range staged {
			_ = os.Remove(item.stage)
		}
		if err != nil {
			return result, err
		}
		if err := v.store.Transaction(func(tx *store.Tx) error {
			for _, item := range staged {
				if item.record {
					if err := tx.PutVaultFile(item.manifest); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			if rollbackErr := rollback(); rollbackErr != nil {
				return result, errors.Join(fmt.Errorf("record rerouted vault manifest: %w", err), fmt.Errorf("restore previous vault bundle: %w", rollbackErr))
			}
			return result, fmt.Errorf("record rerouted vault manifest: %w", err)
		}
		if err := finalize(); err != nil {
			return result, fmt.Errorf("remove rerouted vault rollback link: %w", err)
		}
		for _, item := range staged {
			providerResult := results[item.manifest.Provider]
			providerResult.Archived++
			providerResult.InputBytes += item.manifest.Size
		}
		results[staged[0].manifest.Provider].StoredBytes += stored
	}
	now := v.now().Unix()
	if err := v.store.Transaction(func(tx *store.Tx) error {
		if err := tx.SetMeta(lastArchiveMeta, fmt.Sprint(now)); err != nil {
			return err
		}
		return setBrokenArchivesMeta(tx, brokenArchives)
	}); err != nil {
		return result, err
	}
	for _, provider := range []discover.Provider{discover.ProviderCodex, discover.ProviderClaude} {
		if value := results[provider]; value != nil {
			result.Providers = append(result.Providers, *value)
		}
	}
	return result, nil
}

func (v *Vault) cleanupAbandonedStaging() error {
	patterns := []string{
		filepath.Join(v.dir, ".source-*.zst"),
		filepath.Join(v.dir, "*", ".bundle-*.tar.zst"),
		filepath.Join(v.dir, "*", ".history-member-*"),
	}
	for _, pattern := range patterns {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("find abandoned vault staging files: %w", err)
		}
		for _, path := range paths {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("remove abandoned vault staging file %s: %w", path, err)
			}
		}
	}
	return nil
}

func (v *Vault) recoverRollbackFiles() error {
	paths, err := filepath.Glob(filepath.Join(v.dir, "*", "*.tar.zst.rollback"))
	if err != nil {
		return fmt.Errorf("find vault rollback files: %w", err)
	}
	if len(paths) == 0 {
		return nil
	}
	manifest, err := v.store.VaultFiles()
	if err != nil {
		return err
	}
	for _, rollbackPath := range paths {
		target := strings.TrimSuffix(rollbackPath, ".rollback")
		relative, err := filepath.Rel(v.dir, target)
		if err != nil {
			return err
		}
		archive := filepath.ToSlash(relative)
		var expected []store.VaultFile
		for _, file := range manifest {
			if file.Archive == archive {
				expected = append(expected, file)
			}
		}
		activeValid := false
		if len(expected) > 0 {
			_, failures := v.verifyFiles(expected, true)
			activeValid = len(failures) == 0
		}
		if activeValid {
			if err := os.Remove(rollbackPath); err != nil {
				return fmt.Errorf("remove stale vault rollback %s: %w", rollbackPath, err)
			}
			continue
		}
		if err := replaceFile(rollbackPath, target); err != nil {
			return fmt.Errorf("restore vault rollback %s: %w", rollbackPath, err)
		}
	}
	return nil
}

func (v *Vault) loadBrokenArchives() (map[string]bool, error) {
	result := make(map[string]bool)
	value, err := v.store.Meta(brokenArchivesMeta)
	if err != nil || value == "" {
		return result, err
	}
	var archives []string
	if err := json.Unmarshal([]byte(value), &archives); err != nil {
		return nil, fmt.Errorf("read broken vault archive state: %w", err)
	}
	for _, archive := range archives {
		result[archive] = true
	}
	return result, nil
}

func setBrokenArchivesMeta(tx *store.Tx, archives map[string]bool) error {
	if len(archives) == 0 {
		return tx.DeleteMeta(brokenArchivesMeta)
	}
	values := make([]string, 0, len(archives))
	for archive := range archives {
		values = append(values, archive)
	}
	sort.Strings(values)
	data, _ := json.Marshal(values)
	return tx.SetMeta(brokenArchivesMeta, string(data))
}

func stagedCoversManifest(staged []stagedFile, manifest []store.VaultFile) bool {
	covered := make(map[string]bool, len(staged))
	for _, item := range staged {
		covered[manifestKey(item.manifest)] = true
	}
	for _, item := range manifest {
		if !covered[manifestKey(item)] {
			return false
		}
	}
	return len(manifest) > 0
}

func relativeMember(root, source string) (string, error) {
	rel, err := filepath.Rel(root, source)
	if err != nil {
		return "", err
	}
	rel = normalizeMemberPath(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") || rel == "." {
		return "", fmt.Errorf("source %s is outside provider root %s", source, root)
	}
	return rel, nil
}

func normalizeMemberPath(path string) string {
	return strings.ReplaceAll(filepath.ToSlash(path), `\`, "/")
}

func (v *Vault) stage(source discover.SourceFile, rel string, latest store.VaultFile, found, repair bool) (stagedFile, bool, error) {
	before, err := os.Stat(source.Path)
	if err != nil {
		return stagedFile{}, false, err
	}
	if source.Size != before.Size() || source.ModTime.UnixNano() != before.ModTime().UnixNano() {
		return stagedFile{}, true, nil
	}
	if v.beforeRead != nil {
		v.beforeRead(source.Path)
	}
	temp, err := os.CreateTemp(v.dir, ".source-*.zst")
	if err != nil {
		return stagedFile{}, false, err
	}
	stagePath := temp.Name()
	keep := false
	defer func() {
		_ = temp.Close()
		if !keep {
			_ = os.Remove(stagePath)
		}
	}()
	encoder, err := zstd.NewWriter(temp, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return stagedFile{}, false, err
	}
	file, err := os.Open(source.Path)
	if err != nil {
		encoder.Close()
		return stagedFile{}, false, err
	}
	hash := sha256.New()
	stats := &lineStats{}
	_, copyErr := io.Copy(io.MultiWriter(encoder, hash, stats), file)
	stats.finish()
	closeFileErr := file.Close()
	closeEncoderErr := encoder.Close()
	if copyErr != nil {
		return stagedFile{}, false, copyErr
	}
	if closeFileErr != nil {
		return stagedFile{}, false, closeFileErr
	}
	if closeEncoderErr != nil {
		return stagedFile{}, false, closeEncoderErr
	}
	if err := temp.Sync(); err != nil {
		return stagedFile{}, false, err
	}
	if err := temp.Close(); err != nil {
		return stagedFile{}, false, err
	}
	after, err := os.Stat(source.Path)
	if err != nil {
		return stagedFile{}, false, err
	}
	if before.Size() != after.Size() || before.ModTime().UnixNano() != after.ModTime().UnixNano() {
		return stagedFile{}, true, nil
	}
	contentHash := hex.EncodeToString(hash.Sum(nil))
	if found && latest.ContentSHA256 == contentHash {
		if repair {
			keep = true
			return stagedFile{stage: stagePath, manifest: latest}, false, nil
		}
		if err := v.store.Transaction(func(tx *store.Tx) error {
			return tx.UpdateVaultFileSourceState(source.Path, latest.Version, after.Size(), after.ModTime().UnixNano())
		}); err != nil {
			return stagedFile{}, false, err
		}
		return stagedFile{}, false, nil
	}
	version := 1
	if found {
		version = latest.Version + 1
	}
	month := after.ModTime().Format("2006-01")
	archive := filepath.ToSlash(filepath.Join(string(source.Provider), month+".tar.zst"))
	keep = true
	return stagedFile{stage: stagePath, record: true, manifest: store.VaultFile{
		SourcePath: source.Path, Provider: source.Provider, RelPath: rel, Archive: archive,
		ContentSHA256: contentHash, Size: after.Size(), ModTimeUnix: after.ModTime().UnixNano(),
		FirstTS: stats.firstString(), LastTS: stats.lastString(), LineCount: stats.lines,
		VaultedAt: v.now().Unix(), Version: version,
	}}, false, nil
}

type lineStats struct {
	buffer    []byte
	oversized bool
	first     time.Time
	last      time.Time
	lines     int64
}

func (s *lineStats) Write(data []byte) (int, error) {
	const maxTimestampLine = 1 << 20
	written := len(data)
	for len(data) > 0 {
		index := bytes.IndexByte(data, '\n')
		segment := data
		if index >= 0 {
			segment = data[:index]
		}
		if !s.oversized {
			if len(s.buffer)+len(segment) <= maxTimestampLine {
				s.buffer = append(s.buffer, segment...)
			} else {
				s.buffer = nil
				s.oversized = true
			}
		}
		if index < 0 {
			break
		}
		s.finishLine()
		data = data[index+1:]
	}
	return written, nil
}

func (s *lineStats) finishLine() {
	if s.oversized {
		s.lines++
	} else {
		s.record(s.buffer)
	}
	s.buffer = s.buffer[:0]
	s.oversized = false
}

func (s *lineStats) record(line []byte) {
	s.lines++
	var event struct {
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(line, &event) != nil || event.Timestamp == "" {
		return
	}
	timestamp, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil {
		return
	}
	timestamp = timestamp.UTC()
	if s.first.IsZero() || timestamp.Before(s.first) {
		s.first = timestamp
	}
	if s.last.IsZero() || timestamp.After(s.last) {
		s.last = timestamp
	}
}

func (s *lineStats) firstString() string {
	if s.first.IsZero() {
		return ""
	}
	return s.first.Format(time.RFC3339Nano)
}

func (s *lineStats) lastString() string {
	if s.last.IsZero() {
		return ""
	}
	return s.last.Format(time.RFC3339Nano)
}

func (s *lineStats) finish() {
	if len(s.buffer) > 0 || s.oversized {
		s.finishLine()
		s.buffer = nil
	}
}

func (v *Vault) rewriteArchive(relative string, staged []stagedFile, rebuild bool) (int64, func() error, func() error, error) {
	path := filepath.Join(v.dir, filepath.FromSlash(relative))
	var oldSize int64
	hadExisting := false
	if existing, err := os.Stat(path); err == nil {
		oldSize = existing.Size()
		hadExisting = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, nil, nil, err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".bundle-*.tar.zst")
	if err != nil {
		return 0, nil, nil, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	encoder, err := zstd.NewWriter(temp, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithWindowSize(128<<20))
	if err != nil {
		temp.Close()
		return 0, nil, nil, err
	}
	tarWriter := tar.NewWriter(encoder)
	closed := false
	defer func() {
		if !closed {
			_ = tarWriter.Close()
			_ = encoder.Close()
			_ = temp.Close()
		}
	}()
	if _, statErr := os.Stat(path); statErr == nil {
		if !rebuild {
			if err := copyArchive(path, tarWriter); err != nil {
				tarWriter.Close()
				encoder.Close()
				temp.Close()
				return 0, nil, nil, fmt.Errorf("read existing vault bundle %s: %w", path, err)
			}
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return 0, nil, nil, statErr
	}
	for _, item := range staged {
		readerFile, err := os.Open(item.stage)
		if err != nil {
			return 0, nil, nil, err
		}
		decoder, err := zstd.NewReader(readerFile)
		if err != nil {
			readerFile.Close()
			return 0, nil, nil, err
		}
		header := &tar.Header{Name: item.manifest.RelPath, Mode: 0o600, Size: item.manifest.Size, ModTime: time.Unix(0, item.manifest.ModTimeUnix)}
		if err := tarWriter.WriteHeader(header); err != nil {
			decoder.Close()
			readerFile.Close()
			return 0, nil, nil, err
		}
		_, err = io.Copy(tarWriter, decoder)
		decoder.Close()
		readerFile.Close()
		if err != nil {
			return 0, nil, nil, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return 0, nil, nil, err
	}
	if err := encoder.Close(); err != nil {
		return 0, nil, nil, err
	}
	if err := temp.Sync(); err != nil {
		return 0, nil, nil, err
	}
	if err := temp.Close(); err != nil {
		return 0, nil, nil, err
	}
	closed = true
	info, err := os.Stat(tempPath)
	if err != nil {
		return 0, nil, nil, err
	}
	rollbackPath := path + ".rollback"
	if hadExisting {
		if err := os.Link(path, rollbackPath); err != nil {
			return 0, nil, nil, fmt.Errorf("preserve existing vault bundle for rollback: %w", err)
		}
	}
	if err := replaceFile(tempPath, path); err != nil {
		if hadExisting {
			_ = os.Remove(rollbackPath)
		}
		return 0, nil, nil, err
	}
	finalize := func() error {
		if hadExisting {
			return os.Remove(rollbackPath)
		}
		return nil
	}
	rollback := func() error {
		if hadExisting {
			return replaceFile(rollbackPath, path)
		}
		return os.Remove(path)
	}
	return info.Size() - oldSize, finalize, rollback, nil
}

func copyArchive(path string, writer *tar.Writer) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder, err := zstd.NewReader(file)
	if err != nil {
		return err
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		copyHeader := *header
		if err := writer.WriteHeader(&copyHeader); err != nil {
			return err
		}
		if _, err := io.Copy(writer, reader); err != nil {
			return err
		}
	}
}
