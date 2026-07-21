package vault

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

type VerifyFailure struct {
	SourcePath string `json:"source_path"`
	Version    int    `json:"version"`
	Archive    string `json:"archive"`
	Error      string `json:"error"`
}

type VerifyResult struct {
	Deep     bool            `json:"deep"`
	Checked  int             `json:"checked"`
	Verified int             `json:"verified"`
	Failures []VerifyFailure `json:"failures"`
}

func (v *Vault) Verify(deep bool) (VerifyResult, error) {
	if _, err := v.EnsureFormat(); err != nil {
		return VerifyResult{}, err
	}
	release, err := store.LockPath(filepath.Join(v.dir, ".tokenomnom-vault.lock"))
	if err != nil {
		return VerifyResult{}, err
	}
	defer release()
	files, err := v.store.VaultFiles()
	if err != nil {
		return VerifyResult{}, err
	}
	verified, failures := v.verifyFiles(files, deep)
	result := VerifyResult{Deep: deep, Checked: len(files), Verified: len(verified), Failures: failures}
	if err := v.recordVerificationFailures(failures, deep); err != nil {
		return result, err
	}
	if len(failures) > 0 {
		return result, fmt.Errorf("vault verification failed for %d file(s)", len(failures))
	}
	return result, nil
}

func (v *Vault) recordVerificationFailures(failures []VerifyFailure, deep bool) error {
	broken, err := v.loadBrokenArchives()
	if err != nil {
		return err
	}
	if deep {
		clear(broken)
	}
	for _, failure := range failures {
		broken[failure.Archive] = true
	}
	return v.store.Transaction(func(tx *store.Tx) error { return setBrokenArchivesMeta(tx, broken) })
}

func manifestKey(file store.VaultFile) string {
	return fmt.Sprintf("%s\x00%d", file.SourcePath, file.Version)
}

func (v *Vault) verifyFiles(files []store.VaultFile, deep bool) (map[string]bool, []VerifyFailure) {
	verified := make(map[string]bool, len(files))
	groups := make(map[string][]store.VaultFile)
	for _, file := range files {
		groups[file.Archive] = append(groups[file.Archive], file)
	}
	archives := make([]string, 0, len(groups))
	for archive := range groups {
		archives = append(archives, archive)
	}
	sort.Strings(archives)
	var failures []VerifyFailure
	for _, archive := range archives {
		wanted := groups[archive]
		path := filepath.Join(v.dir, filepath.FromSlash(archive))
		if err := scanBundle(path, wanted, deep, verified); err != nil {
			for _, file := range wanted {
				delete(verified, manifestKey(file))
				failures = append(failures, failure(file, err))
			}
			continue
		}
		for _, file := range wanted {
			if !verified[manifestKey(file)] {
				failures = append(failures, failure(file, errors.New("tar member missing or does not match manifest")))
			}
		}
	}
	if failures == nil {
		failures = []VerifyFailure{}
	}
	return verified, failures
}

func failure(file store.VaultFile, err error) VerifyFailure {
	return VerifyFailure{SourcePath: file.SourcePath, Version: file.Version, Archive: file.Archive, Error: err.Error()}
}

func scanBundle(path string, wanted []store.VaultFile, deep bool, verified map[string]bool) error {
	byMember := make(map[string][]store.VaultFile)
	for _, candidate := range wanted {
		byMember[candidate.RelPath] = append(byMember[candidate.RelPath], candidate)
	}
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
		var candidates []store.VaultFile
		for _, candidate := range byMember[header.Name] {
			if candidate.Size == header.Size && !verified[manifestKey(candidate)] {
				candidates = append(candidates, candidate)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		if !deep {
			if _, err := io.Copy(io.Discard, reader); err != nil {
				return err
			}
			for _, candidate := range candidates {
				verified[manifestKey(candidate)] = true
				break
			}
			continue
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, reader); err != nil {
			return err
		}
		digest := hex.EncodeToString(hash.Sum(nil))
		for _, candidate := range candidates {
			if candidate.ContentSHA256 == digest {
				verified[manifestKey(candidate)] = true
				break
			}
		}
	}
}

type ListFilter struct {
	Provider discover.Provider
	Since    *time.Time
	Until    *time.Time
	Sort     store.VaultSort
}

type ListPageQuery struct {
	ListFilter
	Sort       store.VaultSort
	Limit      int
	Cursor     string
	LatestOnly bool
}

type ListEntry struct {
	store.VaultFile
	OriginalExists bool `json:"original_exists"`
}

func (v *Vault) List(filter ListFilter) ([]ListEntry, error) {
	if _, err := v.EnsureFormat(); err != nil {
		return nil, err
	}
	files, err := v.store.VaultFiles()
	if err != nil {
		return nil, err
	}
	entries := make([]ListEntry, 0, len(files))
	for _, file := range files {
		if filter.Provider != "" && file.Provider != filter.Provider {
			continue
		}
		if filter.Since != nil && file.LastTS != "" {
			last, err := time.Parse(time.RFC3339Nano, file.LastTS)
			if err == nil && last.Before(*filter.Since) {
				continue
			}
		}
		if filter.Until != nil && file.FirstTS != "" {
			first, err := time.Parse(time.RFC3339Nano, file.FirstTS)
			if err == nil && first.After(*filter.Until) {
				continue
			}
		}
		_, statErr := os.Stat(file.SourcePath)
		entries = append(entries, ListEntry{VaultFile: file, OriginalExists: statErr == nil})
	}
	if filter.Sort != "" {
		sortListEntries(entries, filter.Sort)
	}
	return entries, nil
}

func sortListEntries(entries []ListEntry, sortBy store.VaultSort) {
	sort.SliceStable(entries, func(i, j int) bool {
		left, right := entries[i].VaultFile, entries[j].VaultFile
		if sortBy == store.VaultSortSource {
			if left.SourcePath != right.SourcePath {
				return left.SourcePath < right.SourcePath
			}
			return left.Version < right.Version
		}
		if sortBy == store.VaultSortSize && left.Size != right.Size {
			return left.Size > right.Size
		}
		if sortBy == store.VaultSortFirstTS || sortBy == store.VaultSortLastTS {
			leftValue, rightValue := left.LastTS, right.LastTS
			if sortBy == store.VaultSortFirstTS {
				leftValue, rightValue = left.FirstTS, right.FirstTS
			}
			leftTime, leftErr := time.Parse(time.RFC3339Nano, leftValue)
			rightTime, rightErr := time.Parse(time.RFC3339Nano, rightValue)
			if (leftErr == nil) != (rightErr == nil) {
				return leftErr == nil
			}
			if leftErr == nil && !leftTime.Equal(rightTime) {
				return leftTime.After(rightTime)
			}
		}
		if left.SourcePath != right.SourcePath {
			return left.SourcePath < right.SourcePath
		}
		return left.Version > right.Version
	})
}

type ListPage struct {
	Entries    []ListEntry
	Limit      int
	HasMore    bool
	NextCursor string
}

// ListPage returns a SQL-filtered page and stats only its returned originals.
func (v *Vault) ListPage(query ListPageQuery) (ListPage, error) {
	if _, err := v.EnsureFormat(); err != nil {
		return ListPage{}, err
	}
	page, err := v.store.VaultFilesPage(store.VaultFileQuery{
		Provider: query.Provider, Since: query.Since, Until: query.Until,
		Sort: query.Sort, Limit: query.Limit, Cursor: query.Cursor, LatestOnly: query.LatestOnly,
	})
	if err != nil {
		return ListPage{}, err
	}
	entries := make([]ListEntry, 0, len(page.Files))
	for _, file := range page.Files {
		_, statErr := os.Stat(file.SourcePath)
		entries = append(entries, ListEntry{VaultFile: file, OriginalExists: statErr == nil})
	}
	return ListPage{Entries: entries, Limit: page.Limit, HasMore: page.HasMore, NextCursor: page.NextCursor}, nil
}

func (v *Vault) Resolve(name string, version int) (store.VaultFile, error) {
	files, err := v.store.VaultFiles()
	if err != nil {
		return store.VaultFile{}, err
	}
	normalized := filepath.ToSlash(name)
	resolvedName := name
	if filepath.IsAbs(name) {
		if resolved, err := filepath.EvalSymlinks(name); err == nil {
			resolvedName = resolved
		}
	}
	var matches []store.VaultFile
	for _, file := range files {
		if file.SourcePath != name && file.SourcePath != resolvedName && file.RelPath != normalized {
			continue
		}
		if version > 0 && file.Version != version {
			continue
		}
		matches = append(matches, file)
	}
	if len(matches) == 0 {
		if version > 0 {
			return store.VaultFile{}, fmt.Errorf("vault file %q version %d not found", name, version)
		}
		return store.VaultFile{}, fmt.Errorf("vault file %q not found", name)
	}
	sources := make(map[string]bool)
	for _, match := range matches {
		sources[match.SourcePath] = true
	}
	if len(sources) > 1 {
		return store.VaultFile{}, fmt.Errorf("vault path %q is ambiguous; use the absolute source path", name)
	}
	selected := matches[0]
	for _, match := range matches[1:] {
		if match.Version > selected.Version {
			selected = match
		}
	}
	return selected, nil
}

func (v *Vault) Cat(name string, version int, output io.Writer) (store.VaultFile, error) {
	if _, err := v.EnsureFormat(); err != nil {
		return store.VaultFile{}, err
	}
	manifest, err := v.Resolve(name, version)
	if err != nil {
		return store.VaultFile{}, err
	}
	path := filepath.Join(v.dir, filepath.FromSlash(manifest.Archive))
	file, err := os.Open(path)
	if err != nil {
		return store.VaultFile{}, err
	}
	defer file.Close()
	decoder, err := zstd.NewReader(file)
	if err != nil {
		return store.VaultFile{}, err
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return store.VaultFile{}, err
		}
		if header.Name != manifest.RelPath || header.Size != manifest.Size {
			continue
		}
		temp, err := os.CreateTemp(v.dir, ".cat-*")
		if err != nil {
			return store.VaultFile{}, err
		}
		tempPath := temp.Name()
		hash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(temp, hash), reader)
		closeErr := temp.Close()
		if copyErr != nil || closeErr != nil {
			os.Remove(tempPath)
			if copyErr != nil {
				return store.VaultFile{}, copyErr
			}
			return store.VaultFile{}, closeErr
		}
		if hex.EncodeToString(hash.Sum(nil)) != manifest.ContentSHA256 {
			os.Remove(tempPath)
			continue
		}
		temp, err = os.Open(tempPath)
		if err == nil {
			_, err = io.Copy(output, temp)
			temp.Close()
		}
		os.Remove(tempPath)
		if err != nil {
			return store.VaultFile{}, err
		}
		return manifest, nil
	}
	return store.VaultFile{}, fmt.Errorf("vault member %s version %d is missing or corrupt", manifest.RelPath, manifest.Version)
}

type ProviderStatus struct {
	Provider         discover.Provider `json:"provider"`
	Files            int               `json:"files"`
	RawBytes         int64             `json:"raw_bytes"`
	StoredBytes      int64             `json:"stored_bytes"`
	ReclaimableBytes int64             `json:"reclaimable_bytes"`
}

type Status struct {
	Dir                    string           `json:"dir"`
	Format                 int              `json:"format"`
	Encryption             string           `json:"encryption"`
	Files                  int              `json:"files"`
	RawBytes               int64            `json:"raw_bytes"`
	StoredBytes            int64            `json:"stored_bytes"`
	Ratio                  float64          `json:"ratio"`
	ReclaimableBytes       int64            `json:"reclaimable_bytes"`
	ReclaimablePaths       []string         `json:"reclaimable_paths"`
	Providers              []ProviderStatus `json:"providers"`
	NeverDeletesSources    bool             `json:"never_deletes_sources"`
	ReclaimableInstruction string           `json:"reclaimable_instruction"`
}

func (v *Vault) Status() (Status, error) {
	marker, err := v.EnsureFormat()
	if err != nil {
		return Status{}, err
	}
	release, err := store.LockPath(filepath.Join(v.dir, ".tokenomnom-vault.lock"))
	if err != nil {
		return Status{}, err
	}
	defer release()
	files, err := v.store.VaultFiles()
	if err != nil {
		return Status{}, err
	}
	status := Status{
		Dir: v.dir, Format: marker.VaultFormat, Encryption: marker.Encryption, Files: len(files), ReclaimablePaths: []string{},
		NeverDeletesSources: true, ReclaimableInstruction: "tokenomnom never deletes source files; you may reclaim the listed paths manually",
	}
	providerStatus := map[discover.Provider]*ProviderStatus{}
	archives := make(map[string]discover.Provider)
	for _, file := range files {
		current := providerStatus[file.Provider]
		if current == nil {
			current = &ProviderStatus{Provider: file.Provider}
			providerStatus[file.Provider] = current
		}
		current.Files++
		current.RawBytes += file.Size
		status.RawBytes += file.Size
		archives[file.Archive] = file.Provider
	}
	latestFiles, err := v.store.LatestVaultFiles()
	if err != nil {
		return Status{}, err
	}
	verified, _ := v.verifyFiles(latestFiles, true)
	for _, file := range latestFiles {
		info, statErr := os.Stat(file.SourcePath)
		if statErr == nil && info.Size() == file.Size && info.ModTime().UnixNano() == file.ModTimeUnix &&
			verified[manifestKey(file)] && sourceMatchesManifest(file) {
			providerStatus[file.Provider].ReclaimableBytes += file.Size
			status.ReclaimableBytes += file.Size
			status.ReclaimablePaths = append(status.ReclaimablePaths, file.SourcePath)
		}
	}
	for archive, provider := range archives {
		info, err := os.Stat(filepath.Join(v.dir, filepath.FromSlash(archive)))
		if err == nil {
			status.StoredBytes += info.Size()
			providerStatus[provider].StoredBytes += info.Size()
		}
	}
	if status.StoredBytes > 0 {
		status.Ratio = float64(status.RawBytes) / float64(status.StoredBytes)
	}
	sort.Strings(status.ReclaimablePaths)
	for _, provider := range []discover.Provider{discover.ProviderCodex, discover.ProviderClaude} {
		if current := providerStatus[provider]; current != nil {
			status.Providers = append(status.Providers, *current)
		}
	}
	if err := v.store.Transaction(func(tx *store.Tx) error {
		if err := tx.SetMeta("last_vault_reclaimable_bytes", strconv.FormatInt(status.ReclaimableBytes, 10)); err != nil {
			return err
		}
		return tx.SetMeta("last_vault_status_unix", strconv.FormatInt(v.now().Unix(), 10))
	}); err != nil {
		return Status{}, err
	}
	return status, nil
}

// Snapshot returns lightweight vault totals and the last deeply verified reclaimable value.
func (v *Vault) Snapshot() (Status, int64, error) {
	marker, err := v.EnsureFormat()
	if err != nil {
		return Status{}, 0, err
	}
	release, err := store.LockPath(filepath.Join(v.dir, ".tokenomnom-vault.lock"))
	if err != nil {
		return Status{}, 0, err
	}
	defer release()
	files, err := v.store.VaultFiles()
	if err != nil {
		return Status{}, 0, err
	}
	status := Status{Dir: v.dir, Format: marker.VaultFormat, Encryption: marker.Encryption, Files: len(files)}
	archives := make(map[string]bool)
	for _, file := range files {
		status.RawBytes += file.Size
		archives[file.Archive] = true
	}
	for archive := range archives {
		if info, err := os.Stat(filepath.Join(v.dir, filepath.FromSlash(archive))); err == nil {
			status.StoredBytes += info.Size()
		}
	}
	if status.StoredBytes > 0 {
		status.Ratio = float64(status.RawBytes) / float64(status.StoredBytes)
	}
	if value, err := v.store.Meta("last_vault_reclaimable_bytes"); err != nil {
		return Status{}, 0, err
	} else if value != "" {
		status.ReclaimableBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return Status{}, 0, fmt.Errorf("parse cached vault reclaimable bytes: %w", err)
		}
	}
	var cachedAt int64
	if value, err := v.store.Meta("last_vault_status_unix"); err != nil {
		return Status{}, 0, err
	} else if value != "" {
		cachedAt, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return Status{}, 0, fmt.Errorf("parse cached vault status time: %w", err)
		}
	}
	return status, cachedAt, nil
}

func sourceMatchesManifest(manifest store.VaultFile) bool {
	before, err := os.Stat(manifest.SourcePath)
	if err != nil || before.Size() != manifest.Size || before.ModTime().UnixNano() != manifest.ModTimeUnix {
		return false
	}
	file, err := os.Open(manifest.SourcePath)
	if err != nil {
		return false
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return false
	}
	after, err := os.Stat(manifest.SourcePath)
	if err != nil || before.Size() != after.Size() || before.ModTime().UnixNano() != after.ModTime().UnixNano() {
		return false
	}
	return hex.EncodeToString(hash.Sum(nil)) == manifest.ContentSHA256
}
