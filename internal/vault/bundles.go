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
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

// BundleQuery selects immutable vault manifest entries for traversal.
type BundleQuery struct {
	Providers []discover.Provider
	// Skip runs under the vault and consumer locks before an archive is opened.
	Skip func(archive string, members []store.VaultFile) (bool, error)
}

// VerifiedMember is one byte-exact manifest member. Content remains valid only
// until the next BundleReader.Next call and must be consumed before advancing.
type VerifiedMember struct {
	Manifest store.VaultFile
	Content  io.Reader
}

// BundleFailure is a bounded, non-content traversal diagnostic.
type BundleFailure struct {
	Archive   string `json:"archive"`
	Error     string `json:"error"`
	Integrity bool   `json:"-"`
}

// BundleWalkResult reports independently successful and failed bundles.
type BundleWalkResult struct {
	SelectedBundles int             `json:"selected_bundles"`
	SelectedMembers int             `json:"selected_members"`
	WalkedBundles   int             `json:"walked_bundles"`
	VerifiedMembers int             `json:"verified_members"`
	FailedBundles   int             `json:"failed_bundles"`
	Failures        []BundleFailure `json:"failures"`
	AllFailures     []BundleFailure `json:"-"`
}

// BundleReader streams one archive exactly once. Next returns io.EOF only
// after every selected manifest entry has been matched by path, size, and hash.
type BundleReader struct {
	archive     string
	wanted      []store.VaultFile
	byMember    map[string][]int
	matched     []bool
	file        *os.File
	decoder     *zstd.Decoder
	tar         *tar.Reader
	completed   bool
	verified    int
	mismatch    string
	tempDir     string
	current     *os.File
	currentPath string
}

type bundleIntegrityError struct{ err error }

func (e bundleIntegrityError) Error() string { return e.err.Error() }
func (e bundleIntegrityError) Unwrap() error { return e.err }

type trackedWriter struct {
	writer io.Writer
	err    error
}

func (w *trackedWriter) Write(data []byte) (int, error) {
	written, err := w.writer.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
	}
	return written, err
}

// Archive is the manifest-relative archive path.
func (r *BundleReader) Archive() string { return r.archive }

// Members returns the selected manifest view for this archive.
func (r *BundleReader) Members() []store.VaultFile {
	return append([]store.VaultFile(nil), r.wanted...)
}

// Next returns the next verified selected member.
func (r *BundleReader) Next() (VerifiedMember, error) {
	r.removeCurrent()
	if r.completed {
		return VerifiedMember{}, io.EOF
	}
	for {
		header, err := r.tar.Next()
		if errors.Is(err, io.EOF) {
			for index, candidate := range r.wanted {
				if !r.matched[index] {
					detail := "missing"
					if r.mismatch != "" {
						detail = r.mismatch
					}
					return VerifiedMember{}, bundleIntegrityError{fmt.Errorf("vault member %s version %d is %s or does not match manifest", candidate.RelPath, candidate.Version, detail)}
				}
			}
			r.completed = true
			return VerifiedMember{}, io.EOF
		}
		if err != nil {
			return VerifiedMember{}, bundleIntegrityError{fmt.Errorf("read vault archive %s: %w", r.archive, err)}
		}
		indexes := r.byMember[header.Name]
		if len(indexes) == 0 {
			continue
		}
		var sizeCandidate bool
		for _, index := range indexes {
			if !r.matched[index] && r.wanted[index].Size == header.Size {
				sizeCandidate = true
				break
			}
		}
		if !sizeCandidate {
			continue
		}
		temp, err := os.CreateTemp(r.tempDir, ".history-member-*")
		if err != nil {
			return VerifiedMember{}, fmt.Errorf("stage verified vault member %s: %w", header.Name, err)
		}
		tempPath := temp.Name()
		hasher := sha256.New()
		tempWriter := &trackedWriter{writer: temp}
		written, copyErr := io.Copy(io.MultiWriter(tempWriter, hasher), r.tar)
		if copyErr != nil {
			temp.Close()
			os.Remove(tempPath)
			if tempWriter.err == nil {
				copyErr = bundleIntegrityError{copyErr}
			}
			return VerifiedMember{}, fmt.Errorf("read vault member %s: %w", header.Name, copyErr)
		}
		if written != header.Size {
			temp.Close()
			os.Remove(tempPath)
			return VerifiedMember{}, bundleIntegrityError{fmt.Errorf("vault member %s size mismatch: read %d, header says %d", header.Name, written, header.Size)}
		}
		hash := hex.EncodeToString(hasher.Sum(nil))
		for _, index := range indexes {
			candidate := r.wanted[index]
			if r.matched[index] || candidate.Size != header.Size || candidate.ContentSHA256 != hash {
				continue
			}
			r.matched[index] = true
			r.verified++
			if _, err := temp.Seek(0, io.SeekStart); err != nil {
				temp.Close()
				os.Remove(tempPath)
				return VerifiedMember{}, fmt.Errorf("rewind verified vault member %s: %w", header.Name, err)
			}
			r.current, r.currentPath = temp, tempPath
			return VerifiedMember{Manifest: candidate, Content: temp}, nil
		}
		temp.Close()
		os.Remove(tempPath)
		r.mismatch = "corrupt"
	}
}

func (r *BundleReader) close() {
	r.removeCurrent()
	if r.decoder != nil {
		r.decoder.Close()
	}
	if r.file != nil {
		_ = r.file.Close()
	}
}

func (r *BundleReader) removeCurrent() {
	if r.current != nil {
		_ = r.current.Close()
		_ = os.Remove(r.currentPath)
		r.current, r.currentPath = nil, ""
	}
}

// WalkVerifiedBundles holds the vault lock across a consistent manifest view,
// consumer-lock acquisition, and every one-pass archive traversal. acquire is
// called after the vault lock is held, which lets combined callers enforce the
// vault -> history -> SQLite transaction lock order without vault importing
// history packages.
func (v *Vault) WalkVerifiedBundles(query BundleQuery, acquire func() (func(), error), visit func(*BundleReader) error) (BundleWalkResult, error) {
	return v.WalkVerifiedBundlesComplete(query, acquire, visit, nil)
}

// WalkVerifiedBundlesComplete is WalkVerifiedBundles with a final callback
// that runs after every independent bundle and before either lock is released.
// Combined history indexing uses it to keep purge and competing index commands
// out through provider reconciliation and run metadata.
func (v *Vault) WalkVerifiedBundlesComplete(query BundleQuery, acquire func() (func(), error), visit func(*BundleReader) error, complete func(BundleWalkResult) error) (BundleWalkResult, error) {
	result := BundleWalkResult{Failures: []BundleFailure{}}
	marker, initialized, err := InspectFormat(v.dir)
	if err != nil {
		return result, err
	}
	if initialized {
		if err := validateMarker(marker); err != nil {
			return result, err
		}
	}
	if err := os.MkdirAll(v.dir, 0o700); err != nil {
		return result, fmt.Errorf("create vault directory: %w", err)
	}
	if err := os.Chmod(v.dir, 0o700); err != nil {
		return result, fmt.Errorf("secure vault directory: %w", err)
	}
	releaseVault, err := store.LockPath(filepath.Join(v.dir, ".tokenomnom-vault.lock"))
	if err != nil {
		return result, err
	}
	defer releaseVault()
	if err := v.cleanupAbandonedStaging(); err != nil {
		return result, err
	}

	manifest, err := v.store.VaultFiles()
	if err != nil {
		return result, err
	}
	selectedProviders := make(map[discover.Provider]bool, len(query.Providers))
	for _, provider := range query.Providers {
		selectedProviders[provider] = true
	}
	groups := make(map[string][]store.VaultFile)
	for _, file := range manifest {
		if len(selectedProviders) > 0 && !selectedProviders[file.Provider] {
			continue
		}
		file.RelPath = normalizeMemberPath(file.RelPath)
		groups[file.Archive] = append(groups[file.Archive], file)
		result.SelectedMembers++
	}
	result.SelectedBundles = len(groups)
	releaseConsumer := func() {}
	if acquire != nil {
		releaseConsumer, err = acquire()
		if err != nil {
			return result, err
		}
	}
	defer releaseConsumer()

	broken, err := v.loadBrokenArchives()
	if err != nil {
		return result, err
	}
	archives := make([]string, 0, len(groups))
	for archive := range groups {
		archives = append(archives, archive)
	}
	sort.Strings(archives)
	for _, archive := range archives {
		wanted := groups[archive]
		sort.Slice(wanted, func(i, j int) bool {
			if wanted[i].RelPath != wanted[j].RelPath {
				return wanted[i].RelPath < wanted[j].RelPath
			}
			if wanted[i].Size != wanted[j].Size {
				return wanted[i].Size < wanted[j].Size
			}
			return wanted[i].ContentSHA256 < wanted[j].ContentSHA256
		})
		if broken[archive] {
			result.addFailure(archive, errors.New("vault bundle is marked broken; run vault verify --deep after repair"), true)
			continue
		}
		if query.Skip != nil {
			skip, err := query.Skip(archive, append([]store.VaultFile(nil), wanted...))
			if err != nil {
				return result, fmt.Errorf("check vault bundle %s checkpoint: %w", archive, err)
			}
			if skip {
				continue
			}
		}
		reader, openErr := openBundleReader(filepath.Join(v.dir, filepath.FromSlash(archive)), archive, wanted)
		if openErr != nil {
			result.addFailure(archive, openErr, isBundleIntegrityError(openErr))
			continue
		}
		result.WalkedBundles++
		visitErr := visit(reader)
		if visitErr == nil && !reader.completed {
			visitErr = errors.New("bundle callback returned before consuming the verified manifest")
		}
		result.VerifiedMembers += reader.verified
		reader.close()
		if visitErr != nil {
			result.addFailure(archive, visitErr, isBundleIntegrityError(visitErr))
		}
	}
	if complete != nil {
		if err := complete(result); err != nil {
			return result, err
		}
	}
	if result.FailedBundles > 0 {
		return result, fmt.Errorf("vault bundle traversal failed for %d bundle(s)", result.FailedBundles)
	}
	return result, nil
}

func openBundleReader(path, archive string, wanted []store.VaultFile) (*BundleReader, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = bundleIntegrityError{err}
		}
		return nil, fmt.Errorf("open vault bundle %s: %w", archive, err)
	}
	decoder, err := zstd.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("decompress vault bundle %s: %w", archive, bundleIntegrityError{err})
	}
	reader := &BundleReader{
		archive: archive, wanted: wanted, byMember: make(map[string][]int), matched: make([]bool, len(wanted)),
		file: file, decoder: decoder, tar: tar.NewReader(decoder), tempDir: filepath.Dir(path),
	}
	for index, candidate := range wanted {
		reader.byMember[normalizeMemberPath(candidate.RelPath)] = append(reader.byMember[normalizeMemberPath(candidate.RelPath)], index)
	}
	return reader, nil
}

func isBundleIntegrityError(err error) bool {
	var integrityErr bundleIntegrityError
	return errors.As(err, &integrityErr)
}

func (r *BundleWalkResult) addFailure(archive string, err error, integrity bool) {
	r.FailedBundles++
	message := strings.TrimSpace(err.Error())
	if len(message) > 2048 {
		message = message[:2048]
	}
	failure := BundleFailure{Archive: archive, Error: message, Integrity: integrity}
	r.AllFailures = append(r.AllFailures, failure)
	if len(r.Failures) < 100 {
		r.Failures = append(r.Failures, failure)
	}
}
