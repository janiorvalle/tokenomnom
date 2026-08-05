package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckFindsPlatformAssetAndUsesGitHubToken(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		fmt.Fprintf(writer, `{"tag_name":"v1.2.0","html_url":"https://example.test/release","assets":[{"name":"tokenomnom_1.2.0_darwin_arm64.tar.gz","browser_download_url":"%s/archive"},{"name":"checksums.txt","browser_download_url":"%s/checksums"}]}`, serverURL(request), serverURL(request))
	}))
	defer server.Close()
	upgrader, err := New(Options{Client: server.Client(), APIBaseURL: server.URL, Repository: "owner/repo", CurrentVersion: "1.1.0", GOOS: "darwin", GOARCH: "arm64", ExecutablePath: "/tmp/tokenomnom", GitHubToken: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	release, available, err := upgrader.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !available || release.Version != "1.2.0" || release.ArchiveName != "tokenomnom_1.2.0_darwin_arm64.tar.gz" {
		t.Fatalf("release = %+v, available=%t", release, available)
	}
	if authorization != "Bearer secret" {
		t.Fatalf("authorization = %q", authorization)
	}
}

func TestInstallVerifiesAndAtomicallyReplacesExecutable(t *testing.T) {
	requireUnixExecutableFixture(t)
	newBinary := versionScript("2.0.0")
	archive := releaseArchive(t, newBinary)
	digest := sha256.Sum256(archive)
	server := assetServer(t, archive, []byte(hex.EncodeToString(digest[:])+"  tokenomnom_2.0.0_linux_amd64.tar.gz\n"))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "tokenomnom")
	if err := os.WriteFile(path, versionScript("1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	siblingPath := filepath.Join(filepath.Dir(path), "nomnom")
	if err := os.WriteFile(siblingPath, versionScript("1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	upgrader, err := New(Options{Client: server.Client(), CurrentVersion: "1.0.0", GOOS: "linux", GOARCH: "amd64", ExecutablePath: path, ManagedSibling: func(string, string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := upgrader.Install(context.Background(), Release{Version: "2.0.0", URL: "https://example.test/v2", ArchiveName: "tokenomnom_2.0.0_linux_amd64.tar.gz", ArchiveURL: server.URL + "/archive", ChecksumsURL: server.URL + "/checksums"})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != string(newBinary) {
		t.Fatalf("installed binary = %q, %v", contents, err)
	}
	siblingContents, err := os.ReadFile(siblingPath)
	if err != nil || string(siblingContents) != string(newBinary) {
		t.Fatalf("installed sibling = %q, %v", siblingContents, err)
	}
	if result.PreviousVersion != "1.0.0" || result.Version != "2.0.0" || result.ExecutablePath != path {
		t.Fatalf("result = %+v", result)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode = %v, %v", info.Mode(), err)
	}
}

func TestInstallLeavesUnmanagedSiblingUntouched(t *testing.T) {
	requireUnixExecutableFixture(t)
	newBinary := versionScript("2.0.0")
	archive := releaseArchive(t, newBinary)
	digest := sha256.Sum256(archive)
	server := assetServer(t, archive, []byte(hex.EncodeToString(digest[:])+"  tokenomnom_2.0.0_linux_amd64.tar.gz\n"))
	defer server.Close()
	directory := t.TempDir()
	path := filepath.Join(directory, "tokenomnom")
	if err := os.WriteFile(path, versionScript("1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	siblingPath := filepath.Join(directory, "nomnom")
	markerPath := filepath.Join(directory, "unrelated-executed")
	unrelated := []byte("#!/bin/sh\ntouch '" + markerPath + "'\nprintf 'another program\\n'\n")
	if err := os.WriteFile(siblingPath, unrelated, 0o755); err != nil {
		t.Fatal(err)
	}
	upgrader, err := New(Options{Client: server.Client(), CurrentVersion: "1.0.0", GOOS: "linux", GOARCH: "amd64", ExecutablePath: path})
	if err != nil {
		t.Fatal(err)
	}
	_, err = upgrader.Install(context.Background(), Release{Version: "2.0.0", ArchiveName: "tokenomnom_2.0.0_linux_amd64.tar.gz", ArchiveURL: server.URL + "/archive", ChecksumsURL: server.URL + "/checksums"})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(siblingPath)
	if err != nil || !bytes.Equal(contents, unrelated) {
		t.Fatalf("unmanaged sibling changed = %q, %v", contents, err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("unmanaged sibling was executed: %v", err)
	}
}

func TestInstallChecksumMismatchLeavesExecutableUntouched(t *testing.T) {
	archive := releaseArchive(t, versionScript("2.0.0"))
	server := assetServer(t, archive, []byte(strings.Repeat("0", 64)+"  tokenomnom_2.0.0_linux_amd64.tar.gz\n"))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "tokenomnom")
	oldBinary := versionScript("1.0.0")
	if err := os.WriteFile(path, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	upgrader, err := New(Options{Client: server.Client(), CurrentVersion: "1.0.0", GOOS: "linux", GOARCH: "amd64", ExecutablePath: path})
	if err != nil {
		t.Fatal(err)
	}
	_, err = upgrader.Install(context.Background(), Release{Version: "2.0.0", ArchiveName: "tokenomnom_2.0.0_linux_amd64.tar.gz", ArchiveURL: server.URL + "/archive", ChecksumsURL: server.URL + "/checksums"})
	var upgradeError *Error
	if !errors.As(err, &upgradeError) || upgradeError.Code != "TOKENOMNOM_UPGRADE_CHECKSUM_MISMATCH" {
		t.Fatalf("error = %v", err)
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil || string(contents) != string(oldBinary) {
		t.Fatalf("existing binary changed = %q, %v", contents, readErr)
	}
}

func TestInstallRejectsBinaryThatFailsSmokeTest(t *testing.T) {
	archive := releaseArchive(t, []byte("not executable content"))
	digest := sha256.Sum256(archive)
	server := assetServer(t, archive, []byte(hex.EncodeToString(digest[:])+"  tokenomnom_2.0.0_linux_amd64.tar.gz\n"))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "tokenomnom")
	oldBinary := versionScript("1.0.0")
	if err := os.WriteFile(path, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	upgrader, err := New(Options{Client: server.Client(), CurrentVersion: "1.0.0", GOOS: "linux", GOARCH: "amd64", ExecutablePath: path})
	if err != nil {
		t.Fatal(err)
	}
	_, err = upgrader.Install(context.Background(), Release{Version: "2.0.0", ArchiveName: "tokenomnom_2.0.0_linux_amd64.tar.gz", ArchiveURL: server.URL + "/archive", ChecksumsURL: server.URL + "/checksums"})
	var upgradeError *Error
	if !errors.As(err, &upgradeError) || upgradeError.Code != "TOKENOMNOM_UPGRADE_BINARY_INVALID" {
		t.Fatalf("error = %v", err)
	}
	contents, _ := os.ReadFile(path)
	if string(contents) != string(oldBinary) {
		t.Fatalf("existing binary changed = %q", contents)
	}
}

func TestStableReleaseIsNewerThanMatchingPrerelease(t *testing.T) {
	comparison, err := compareVersions("2.0.0", "2.0.0-rc.1")
	if err != nil || comparison <= 0 {
		t.Fatalf("comparison = %d, %v", comparison, err)
	}
}

func TestDevelopmentBuildRefusesWithStableCode(t *testing.T) {
	_, err := New(Options{CurrentVersion: "dev", GOOS: "linux", GOARCH: "amd64", ExecutablePath: "/tmp/tokenomnom"})
	var upgradeError *Error
	if !errors.As(err, &upgradeError) || upgradeError.Code != "TOKENOMNOM_UPGRADE_DEV_BUILD" || !strings.Contains(err.Error(), "go build") {
		t.Fatalf("development error = %v", err)
	}
}

func serverURL(request *http.Request) string { return "http://" + request.Host }

func assetServer(t *testing.T, archive, checksums []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/archive":
			writer.Write(archive)
		case "/checksums":
			writer.Write(checksums)
		default:
			http.NotFound(writer, request)
		}
	}))
}

func releaseArchive(t *testing.T, binary []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range []string{"tokenomnom", "nomnom"} {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(binary))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(binary); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func versionScript(version string) []byte {
	return []byte("#!/bin/sh\nprintf 'tokenomnom version " + version + "\\n'\n")
}

func requireUnixExecutableFixture(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("self-upgrade has no Windows target; this fixture exercises Unix executable replacement")
	}
}
