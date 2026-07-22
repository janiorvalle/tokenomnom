package indexer

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/history/testcorpus"
)

func TestParallelClassificationMatchesSerialIndexResults(t *testing.T) {
	corpus := testcorpus.Generate(testcorpus.Spec{Sessions: 40, Prompts: 200, Seed: testcorpus.DefaultSeed})
	roots, err := testcorpus.WriteLiveFiles(filepath.Join(t.TempDir(), "providers"), corpus)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := store.Open(filepath.Join(t.TempDir(), store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer serial.Close()
	parallel, err := store.Open(filepath.Join(t.TempDir(), store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer parallel.Close()
	now := func() time.Time { return time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) }
	serialInitial, serialErr := Index(Options{Store: serial, Roots: roots, Now: now, hashWorkers: 1})
	parallelInitial, parallelErr := Index(Options{Store: parallel, Roots: roots, Now: now, hashWorkers: 4})
	assertEquivalentIndexSummary(t, serialInitial, serialErr, parallelInitial, parallelErr)
	serialUnchanged, serialErr := Index(Options{Store: serial, Roots: roots, Now: now, hashWorkers: 1})
	parallelUnchanged, parallelErr := Index(Options{Store: parallel, Roots: roots, Now: now, hashWorkers: 4})
	assertEquivalentIndexSummary(t, serialUnchanged, serialErr, parallelUnchanged, parallelErr)

	serialStats, err := serial.Stats()
	if err != nil {
		t.Fatal(err)
	}
	parallelStats, err := parallel.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(serialStats, parallelStats) {
		t.Fatalf("serial stats=%+v parallel stats=%+v", serialStats, parallelStats)
	}
	serialCheckpoints, err := serial.Checkpoints()
	if err != nil {
		t.Fatal(err)
	}
	parallelCheckpoints, err := parallel.Checkpoints()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(checkpointContent(serialCheckpoints), checkpointContent(parallelCheckpoints)) {
		t.Fatal("parallel index checkpoints differ from serial results")
	}
}

func assertEquivalentIndexSummary(t *testing.T, serial Summary, serialErr error, parallel Summary, parallelErr error) {
	t.Helper()
	serial.Duration, parallel.Duration = 0, 0
	if serialErr != nil || parallelErr != nil || !reflect.DeepEqual(serial, parallel) {
		t.Fatalf("serial err=%v summary=%+v parallel err=%v summary=%+v", serialErr, serial, parallelErr, parallel)
	}
}

type stableCheckpoint struct {
	Provider, Path, Kind, SourceKind, Hash, HashState, Prefix, Tail, ExtractorState string
	Size, ModTime, CompleteOffset, LineCount                                        int64
	ExtractorVersion                                                                int
	Missing                                                                         bool
}

func checkpointContent(values map[string]store.Checkpoint) map[string]stableCheckpoint {
	result := make(map[string]stableCheckpoint, len(values))
	for key, value := range values {
		result[key] = stableCheckpoint{
			Provider: string(value.Provider), Path: value.Path, Kind: string(value.Kind), SourceKind: value.SourceKind,
			Hash: value.ContentSHA256, HashState: value.ContentHashState, Prefix: value.PrefixFingerprint,
			Tail: value.TailFingerprint, ExtractorState: value.ExtractorState, Size: value.Size,
			ModTime: value.ModTimeUnixNano, CompleteOffset: value.CompleteOffset, LineCount: value.LineCount,
			ExtractorVersion: value.ExtractorVersion, Missing: value.Missing,
		}
	}
	return result
}
