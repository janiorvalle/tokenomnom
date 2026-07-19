package syncer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

func TestParsedFileFingerprintUsesOpenDescriptor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not replace an open file")
	}
	path := filepath.Join(t.TempDir(), "replaced.jsonl")
	oldContents := []byte("old-line\n")
	newContents := []byte("new-line\nextra\n")
	if err := os.WriteFile(path, oldContents, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	replaced := false
	parsed, err := readParsedFile(discover.SourceFile{
		Provider: discover.ProviderCodex, Path: path, Size: info.Size(), ModTime: info.ModTime(),
	}, 0, "", nil, func([]byte) {
		if replaced {
			return
		}
		replaced = true
		replacement := path + ".new"
		if err := os.WriteFile(replacement, newContents, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(t.TempDir(), "old.jsonl")
	if err := os.WriteFile(oldPath, oldContents, 0o600); err != nil {
		t.Fatal(err)
	}
	wantHash, err := tailHash(oldPath, int64(len(oldContents)))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.hash != wantHash || parsed.offset != int64(len(oldContents)) {
		t.Fatalf("parsed version = %+v, want old descriptor hash %q", parsed, wantHash)
	}
	newInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	kind, err := classify(discover.SourceFile{
		Provider: discover.ProviderCodex, Path: path, Size: newInfo.Size(), ModTime: newInfo.ModTime(),
	}, store.Checkpoint{
		Path: path, Provider: discover.ProviderCodex, Size: parsed.source.Size,
		ModTimeUnix: parsed.source.ModTime.UnixNano(), ByteOffset: parsed.offset, TailHash: parsed.hash,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if kind != fileRewritten {
		t.Fatalf("replacement classified as %v, want rewritten", kind)
	}
}

func TestParsedFileMetadataIncludesRecordsAppendedDuringRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "growing.jsonl")
	initial := []byte("one\ntwo\n")
	appended := []byte("three\n")
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	newModTime := time.Now().Add(2 * time.Second).Round(time.Millisecond)
	added := false
	parsed, err := readParsedFile(discover.SourceFile{Provider: discover.ProviderCodex, Path: path}, 0, "", nil, func([]byte) {
		if added {
			return
		}
		added = true
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(appended); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, newModTime, newModTime); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.offset != int64(len(initial)+len(appended)) || parsed.source.Size != parsed.offset {
		t.Fatalf("parsed growing file = %+v", parsed)
	}
	if parsed.source.ModTime.UnixNano() != newModTime.UnixNano() {
		t.Fatalf("parsed mtime = %s, want %s", parsed.source.ModTime, newModTime)
	}
}
