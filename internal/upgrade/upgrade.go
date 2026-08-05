// Package upgrade downloads and atomically installs tokenomnom releases.
package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRepository = "janiorvalle/tokenomnom"
	defaultAPIBaseURL = "https://api.github.com"
)

// Error is an actionable failure returned to a CLI caller.
type Error struct {
	Code      string
	Message   string
	Action    string
	Retryable bool
	Cause     error
}

func (err *Error) Error() string {
	retry := "do not retry until the problem is fixed"
	if err.Retryable {
		retry = "retry is safe"
	}
	return fmt.Sprintf("%s [%s]: %s; %s; %s", err.Message, err.Code, err.Action, retry, correctedExample(err.Code))
}

func (err *Error) Unwrap() error { return err.Cause }

func correctedExample(code string) string {
	if code == "TOKENOMNOM_UPGRADE_DEV_BUILD" {
		return "example: go build ./cmd/tokenomnom or install a release with install.sh"
	}
	return "example: tokenomnom upgrade"
}

// HTTPClient is the subset of http.Client used by Upgrader.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Options configures release discovery and installation.
type Options struct {
	Client         HTTPClient
	Repository     string
	APIBaseURL     string
	CurrentVersion string
	GOOS           string
	GOARCH         string
	ExecutablePath string
	GitHubToken    string
	ManagedSibling func(path, name string) bool
}

// Release describes the assets required for one published version.
type Release struct {
	Version      string `json:"version"`
	URL          string `json:"url"`
	ArchiveName  string `json:"archive_name"`
	ArchiveURL   string `json:"-"`
	ChecksumsURL string `json:"-"`
}

// Result describes an installed binary replacement.
type Result struct {
	PreviousVersion string `json:"previous_version"`
	Version         string `json:"version"`
	ReleaseURL      string `json:"release_url"`
	ExecutablePath  string `json:"executable_path"`
}

// Upgrader checks and installs releases.
type Upgrader struct{ options Options }

// New validates options and returns an Upgrader.
func New(options Options) (*Upgrader, error) {
	if options.CurrentVersion == "" || options.CurrentVersion == "dev" {
		return nil, &Error{
			Code: "TOKENOMNOM_UPGRADE_DEV_BUILD", Message: "this development build cannot upgrade itself",
			Action: "build it again with go build, or install a published release with install.sh",
		}
	}
	if options.Client == nil {
		options.Client = http.DefaultClient
	}
	if options.Repository == "" {
		options.Repository = defaultRepository
	}
	if options.APIBaseURL == "" {
		options.APIBaseURL = defaultAPIBaseURL
	}
	if _, err := AssetName(options.CurrentVersion, options.GOOS, options.GOARCH); err != nil {
		return nil, err
	}
	if options.ExecutablePath == "" {
		return nil, &Error{Code: "TOKENOMNOM_UPGRADE_EXECUTABLE_UNKNOWN", Message: "the running tokenomnom executable could not be located", Action: "reinstall with install.sh"}
	}
	if options.ManagedSibling == nil {
		options.ManagedSibling = isManagedSibling
	}
	return &Upgrader{options: options}, nil
}

// AssetName returns the same release archive name used by install.sh.
func AssetName(version, goos, goarch string) (string, error) {
	version = strings.TrimPrefix(version, "v")
	if goos != "darwin" && goos != "linux" {
		return "", &Error{Code: "TOKENOMNOM_UPGRADE_UNSUPPORTED_PLATFORM", Message: fmt.Sprintf("self-upgrade is not supported on %s/%s", goos, goarch), Action: "download the matching release asset manually"}
	}
	if goarch != "amd64" && goarch != "arm64" {
		return "", &Error{Code: "TOKENOMNOM_UPGRADE_UNSUPPORTED_PLATFORM", Message: fmt.Sprintf("self-upgrade is not supported on %s/%s", goos, goarch), Action: "build tokenomnom from source for this architecture"}
	}
	return fmt.Sprintf("tokenomnom_%s_%s_%s.tar.gz", version, goos, goarch), nil
}

// Check discovers the latest release and reports whether it is newer.
func (upgrader *Upgrader) Check(ctx context.Context) (Release, bool, error) {
	endpoint := strings.TrimSuffix(upgrader.options.APIBaseURL, "/") + "/repos/" + upgrader.options.Repository + "/releases/latest"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Release{}, false, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "tokenomnom/"+upgrader.options.CurrentVersion)
	if upgrader.options.GitHubToken != "" {
		request.Header.Set("Authorization", "Bearer "+upgrader.options.GitHubToken)
	}
	response, err := upgrader.options.Client.Do(request)
	if err != nil {
		return Release{}, false, networkError("contact GitHub for the latest release", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests {
		return Release{}, false, &Error{Code: "TOKENOMNOM_UPGRADE_RATE_LIMITED", Message: "GitHub refused the release check because its rate limit was reached", Action: "set GITHUB_TOKEN and retry, or wait for the rate limit to reset", Retryable: true}
	}
	if response.StatusCode != http.StatusOK {
		return Release{}, false, &Error{Code: "TOKENOMNOM_UPGRADE_RELEASE_LOOKUP_FAILED", Message: fmt.Sprintf("GitHub returned %s while checking the latest release", response.Status), Action: "check https://github.com/janiorvalle/tokenomnom/releases and retry", Retryable: response.StatusCode >= 500}
	}
	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		return Release{}, false, &Error{Code: "TOKENOMNOM_UPGRADE_INVALID_RELEASE", Message: "GitHub returned an unreadable latest release", Action: "retry the upgrade; if it persists, report the release metadata", Retryable: true, Cause: err}
	}
	latest := strings.TrimPrefix(payload.TagName, "v")
	archiveName, err := AssetName(latest, upgrader.options.GOOS, upgrader.options.GOARCH)
	if err != nil {
		return Release{}, false, err
	}
	release := Release{Version: latest, URL: payload.HTMLURL, ArchiveName: archiveName}
	for _, asset := range payload.Assets {
		switch asset.Name {
		case archiveName:
			release.ArchiveURL = asset.URL
		case "checksums.txt":
			release.ChecksumsURL = asset.URL
		}
	}
	if release.ArchiveURL == "" || release.ChecksumsURL == "" {
		return Release{}, false, &Error{Code: "TOKENOMNOM_UPGRADE_ASSET_MISSING", Message: fmt.Sprintf("release v%s does not contain %s and checksums.txt", latest, archiveName), Action: "use install.sh after the release assets finish publishing", Retryable: true}
	}
	comparison, err := compareVersions(latest, upgrader.options.CurrentVersion)
	if err != nil {
		return Release{}, false, err
	}
	return release, comparison > 0, nil
}

// Install downloads, verifies, and atomically replaces the running executable.
func (upgrader *Upgrader) Install(ctx context.Context, release Release) (Result, error) {
	archive, err := upgrader.download(ctx, release.ArchiveURL, 128<<20)
	if err != nil {
		return Result{}, err
	}
	checksums, err := upgrader.download(ctx, release.ChecksumsURL, 4<<20)
	if err != nil {
		return Result{}, err
	}
	want, found := checksumFor(checksums, release.ArchiveName)
	if !found {
		return Result{}, &Error{Code: "TOKENOMNOM_UPGRADE_CHECKSUM_MISSING", Message: fmt.Sprintf("checksums.txt has no entry for %s", release.ArchiveName), Action: "do not install this release; report the incomplete release assets"}
	}
	got := sha256.Sum256(archive)
	if !strings.EqualFold(hex.EncodeToString(got[:]), want) {
		return Result{}, &Error{Code: "TOKENOMNOM_UPGRADE_CHECKSUM_MISMATCH", Message: fmt.Sprintf("checksum verification failed for %s; the existing binary was not changed", release.ArchiveName), Action: "delete any cached download and retry; if it persists, report the release"}
	}
	binaries, err := extractBinaries(archive)
	if err != nil {
		return Result{}, err
	}
	if err := replaceInstalledExecutables(ctx, upgrader.options.ExecutablePath, release.Version, binaries, upgrader.options.ManagedSibling); err != nil {
		return Result{}, err
	}
	return Result{PreviousVersion: upgrader.options.CurrentVersion, Version: release.Version, ReleaseURL: release.URL, ExecutablePath: upgrader.options.ExecutablePath}, nil
}

func (upgrader *Upgrader) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "tokenomnom/"+upgrader.options.CurrentVersion)
	if upgrader.options.GitHubToken != "" {
		request.Header.Set("Authorization", "Bearer "+upgrader.options.GitHubToken)
	}
	response, err := upgrader.options.Client.Do(request)
	if err != nil {
		return nil, networkError("download the release", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &Error{Code: "TOKENOMNOM_UPGRADE_DOWNLOAD_FAILED", Message: fmt.Sprintf("release download returned %s", response.Status), Action: "check the release URL and retry", Retryable: response.StatusCode >= 500}
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, networkError("read the release download", err)
	}
	if int64(len(contents)) > limit {
		return nil, &Error{Code: "TOKENOMNOM_UPGRADE_DOWNLOAD_TOO_LARGE", Message: "the release download exceeded the safety limit", Action: "do not install it; report the unexpected release asset"}
	}
	return contents, nil
}

func networkError(action string, cause error) error {
	return &Error{Code: "TOKENOMNOM_UPGRADE_NETWORK_FAILED", Message: "could not " + action, Action: "check the network connection and retry", Retryable: true, Cause: cause}
}

func checksumFor(contents []byte, name string) (string, bool) {
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		asset := strings.TrimPrefix(fields[1], "*")
		if asset == name && len(fields[0]) == sha256.Size*2 {
			if _, err := hex.DecodeString(fields[0]); err == nil {
				return fields[0], true
			}
		}
	}
	return "", false
}

func extractBinaries(archive []byte) (map[string][]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, &Error{Code: "TOKENOMNOM_UPGRADE_INVALID_ARCHIVE", Message: "the verified release archive is not valid gzip data", Action: "report the broken release asset", Cause: err}
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	binaries := make(map[string][]byte, 2)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, &Error{Code: "TOKENOMNOM_UPGRADE_INVALID_ARCHIVE", Message: "the verified release archive could not be read", Action: "report the broken release asset", Cause: err}
		}
		name := filepath.Base(header.Name)
		if header.Typeflag == tar.TypeReg && (name == "tokenomnom" || name == "nomnom") {
			if header.Size > 128<<20 {
				return nil, &Error{Code: "TOKENOMNOM_UPGRADE_INVALID_ARCHIVE", Message: fmt.Sprintf("the %s binary exceeded the safety limit", name), Action: "report the broken release asset"}
			}
			contents, err := io.ReadAll(io.LimitReader(tarReader, header.Size))
			if err != nil {
				return nil, &Error{Code: "TOKENOMNOM_UPGRADE_INVALID_ARCHIVE", Message: fmt.Sprintf("the %s binary could not be read", name), Action: "report the broken release asset", Cause: err}
			}
			binaries[name] = contents
		}
	}
	if len(binaries["tokenomnom"]) == 0 || len(binaries["nomnom"]) == 0 {
		return nil, &Error{Code: "TOKENOMNOM_UPGRADE_INVALID_ARCHIVE", Message: "the verified release archive did not contain both tokenomnom and nomnom", Action: "report the broken release asset"}
	}
	return binaries, nil
}

type stagedReplacement struct {
	path      string
	temporary string
	backup    string
	replaced  bool
}

func replaceInstalledExecutables(ctx context.Context, runningPath, version string, binaries map[string][]byte, managedSibling func(path, name string) bool) error {
	runningName := filepath.Base(runningPath)
	archiveName := runningName
	if archiveName != "tokenomnom" && archiveName != "nomnom" {
		archiveName = "tokenomnom"
	}
	targets := map[string][]byte{runningPath: binaries[archiveName]}
	if runningName == "tokenomnom" || runningName == "nomnom" {
		siblingName := "nomnom"
		if runningName == "nomnom" {
			siblingName = "tokenomnom"
		}
		siblingPath := filepath.Join(filepath.Dir(runningPath), siblingName)
		if info, err := os.Stat(siblingPath); err == nil && info.Mode().IsRegular() && managedSibling(siblingPath, siblingName) {
			targets[siblingPath] = binaries[siblingName]
		} else if err != nil && !os.IsNotExist(err) {
			return notWritable(siblingPath, err)
		}
	}
	replacements := make([]stagedReplacement, 0, len(targets))
	for path, contents := range targets {
		replacement, err := stageReplacement(ctx, path, version, contents)
		if err != nil {
			cleanupReplacements(replacements)
			return err
		}
		replacements = append(replacements, replacement)
	}
	for index := range replacements {
		if err := os.Rename(replacements[index].temporary, replacements[index].path); err != nil {
			rollbackErr := rollbackReplacements(replacements[:index])
			cleanupReplacements(replacements)
			if rollbackErr != nil {
				return &Error{Code: "TOKENOMNOM_UPGRADE_ROLLBACK_FAILED", Message: "the binary upgrade failed and the previous installation could not be fully restored", Action: "reinstall immediately with install.sh", Cause: errors.Join(err, rollbackErr)}
			}
			return notWritable(replacements[index].path, err)
		}
		replacements[index].replaced = true
	}
	cleanupReplacements(replacements)
	return nil
}

func isManagedSibling(path, name string) bool {
	info, err := buildinfo.ReadFile(path)
	return err == nil && info.Path == "github.com/janiorvalle/tokenomnom/cmd/"+name
}

func stageReplacement(ctx context.Context, path, version string, contents []byte) (stagedReplacement, error) {
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, ".tokenomnom-upgrade-*")
	if err != nil {
		return stagedReplacement{}, notWritable(path, err)
	}
	tempPath := temp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o755); err != nil {
		temp.Close()
		return stagedReplacement{}, notWritable(path, err)
	}
	if _, err := temp.Write(contents); err != nil {
		temp.Close()
		return stagedReplacement{}, notWritable(path, err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return stagedReplacement{}, notWritable(path, err)
	}
	if err := temp.Close(); err != nil {
		return stagedReplacement{}, notWritable(path, err)
	}
	smokeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(smokeContext, tempPath, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "tokenomnom version "+version {
		return stagedReplacement{}, &Error{Code: "TOKENOMNOM_UPGRADE_BINARY_INVALID", Message: fmt.Sprintf("the verified release binary for %s failed its version smoke test; the existing binary was not changed", filepath.Base(path)), Action: "report the broken release asset", Cause: err}
	}
	backup, err := os.CreateTemp(directory, ".tokenomnom-backup-*")
	if err != nil {
		return stagedReplacement{}, notWritable(path, err)
	}
	backupPath := backup.Name()
	if err := copyExistingFile(path, backup); err != nil {
		backup.Close()
		os.Remove(backupPath)
		return stagedReplacement{}, notWritable(path, err)
	}
	if err := backup.Close(); err != nil {
		os.Remove(backupPath)
		return stagedReplacement{}, notWritable(path, err)
	}
	removeTemp = false
	return stagedReplacement{path: path, temporary: tempPath, backup: backupPath}, nil
}

func copyExistingFile(path string, destination *os.File) error {
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if err := destination.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		return err
	}
	return destination.Sync()
}

func rollbackReplacements(replacements []stagedReplacement) error {
	var rollbackErrors []error
	for index := len(replacements) - 1; index >= 0; index-- {
		if replacements[index].replaced {
			if err := os.Rename(replacements[index].backup, replacements[index].path); err != nil {
				rollbackErrors = append(rollbackErrors, err)
			}
		}
	}
	return errors.Join(rollbackErrors...)
}

func cleanupReplacements(replacements []stagedReplacement) {
	for _, replacement := range replacements {
		os.Remove(replacement.temporary)
		os.Remove(replacement.backup)
	}
}

func notWritable(path string, cause error) error {
	return &Error{Code: "TOKENOMNOM_UPGRADE_NOT_WRITABLE", Message: fmt.Sprintf("the installed binary %q could not be replaced", path), Action: "reinstall with: curl -fsSL https://raw.githubusercontent.com/janiorvalle/tokenomnom/main/install.sh | sh", Cause: cause}
}

func compareVersions(left, right string) (int, error) {
	type parsedVersion struct {
		core       [3]int
		prerelease string
	}
	parse := func(value string) (parsedVersion, error) {
		var result parsedVersion
		value = strings.SplitN(strings.TrimPrefix(value, "v"), "+", 2)[0]
		core, prerelease, _ := strings.Cut(value, "-")
		parts := strings.Split(core, ".")
		if len(parts) != 3 {
			return result, fmt.Errorf("version %q is not major.minor.patch", value)
		}
		for index, part := range parts {
			number, err := strconv.Atoi(part)
			if err != nil || number < 0 {
				return result, fmt.Errorf("version %q is not major.minor.patch", value)
			}
			result.core[index] = number
		}
		result.prerelease = prerelease
		return result, nil
	}
	l, err := parse(left)
	if err != nil {
		return 0, &Error{Code: "TOKENOMNOM_UPGRADE_INVALID_RELEASE", Message: err.Error(), Action: "report the invalid release tag", Cause: err}
	}
	r, err := parse(right)
	if err != nil {
		return 0, &Error{Code: "TOKENOMNOM_UPGRADE_INVALID_VERSION", Message: err.Error(), Action: "reinstall a published tokenomnom release", Cause: err}
	}
	for index := range l.core {
		if l.core[index] > r.core[index] {
			return 1, nil
		}
		if l.core[index] < r.core[index] {
			return -1, nil
		}
	}
	return comparePrereleases(l.prerelease, r.prerelease), nil
}

func comparePrereleases(left, right string) int {
	if left == right {
		return 0
	}
	if left == "" {
		return 1
	}
	if right == "" {
		return -1
	}
	leftParts, rightParts := strings.Split(left, "."), strings.Split(right, ".")
	for index := 0; index < len(leftParts) && index < len(rightParts); index++ {
		if leftParts[index] == rightParts[index] {
			continue
		}
		leftNumber, leftErr := strconv.Atoi(leftParts[index])
		rightNumber, rightErr := strconv.Atoi(rightParts[index])
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber > rightNumber {
				return 1
			}
			return -1
		case leftErr == nil:
			return -1
		case rightErr == nil:
			return 1
		case leftParts[index] > rightParts[index]:
			return 1
		default:
			return -1
		}
	}
	if len(leftParts) > len(rightParts) {
		return 1
	}
	return -1
}
