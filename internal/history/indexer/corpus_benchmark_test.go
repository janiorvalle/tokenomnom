package indexer

import (
	"os"
	"path/filepath"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/history/testcorpus"
)

func BenchmarkCorpusUnchangedIndex(b *testing.B) {
	b.StopTimer()
	corpus := testcorpus.Generate(testcorpus.DefaultSpec())
	root := b.TempDir()
	roots, err := testcorpus.WriteLiveFiles(filepath.Join(root, "providers"), corpus)
	if err != nil {
		b.Fatal(err)
	}
	database, err := store.Open(filepath.Join(root, "state", store.DatabaseName))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = database.Close() })
	now := func() time.Time { return time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) }
	initial, err := Index(Options{Store: database, Roots: roots, Now: now})
	if err != nil || initial.IndexedSources != len(corpus.Sessions) || initial.IndexedPrompts != corpus.Prompts {
		b.Fatalf("initial corpus index err=%v summary=%+v", err, initial)
	}
	for _, benchmark := range []struct {
		name    string
		workers int
	}{{"serial", 1}, {"parallel", 0}} {
		b.Run(benchmark.name, func(b *testing.B) {
			if profilePath := os.Getenv("TOKENOMNOM_CORPUS_CPU_PROFILE"); profilePath != "" && benchmark.name == "parallel" {
				profile, err := os.Create(profilePath)
				if err != nil {
					b.Fatal(err)
				}
				if err := pprof.StartCPUProfile(profile); err != nil {
					_ = profile.Close()
					b.Fatal(err)
				}
				defer func() {
					pprof.StopCPUProfile()
					_ = profile.Close()
				}()
			}
			b.ResetTimer()
			for range b.N {
				summary, err := Index(Options{Store: database, Roots: roots, Now: now, hashWorkers: benchmark.workers})
				if err != nil || summary.SkippedSources != len(corpus.Sessions) || summary.IndexedSources != 0 {
					b.Fatalf("unchanged corpus index err=%v summary=%+v", err, summary)
				}
			}
			b.ReportMetric(float64(len(corpus.Sessions)), "sources")
			b.ReportMetric(float64(corpus.Prompts), "prompts")
		})
	}
}
