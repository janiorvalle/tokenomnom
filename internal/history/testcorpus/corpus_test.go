package testcorpus

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

func TestGenerateIsDeterministicAndCorpusShaped(t *testing.T) {
	spec := Spec{Sessions: 55, Prompts: 270, Seed: DefaultSeed}
	first, second := Generate(spec), Generate(spec)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("fixed corpus seed produced different records")
	}
	if len(first.Sessions) != spec.Sessions || first.Prompts != spec.Prompts {
		t.Fatalf("corpus size=%d/%d want=%d/%d", len(first.Sessions), first.Prompts, spec.Sessions, spec.Prompts)
	}
	providers := map[history.Provider]int{}
	threads := map[history.ThreadKind]int{}
	live, vault, long := 0, 0, 0
	for _, session := range first.Sessions {
		providers[session.Provider]++
		threads[session.ThreadKind]++
		if session.Live {
			live++
		}
		if session.Vault {
			vault++
		}
		for _, prompt := range session.Prompts {
			if len(prompt.Text) > 4000 {
				long++
			}
		}
	}
	if providers[history.ProviderCodex] == 0 || providers[history.ProviderClaude] == 0 ||
		threads[history.ThreadRoot] == 0 || threads[history.ThreadSubagent] == 0 || threads[history.ThreadUnknown] == 0 ||
		live == len(first.Sessions) || vault == 0 || long == 0 {
		t.Fatalf("corpus distributions providers=%v threads=%v live=%d vault=%d long=%d", providers, threads, live, vault, long)
	}
}

func TestDefaultCorpusShape(t *testing.T) {
	corpus := Generate(DefaultSpec())
	cwds, repos, projects := map[string]bool{}, map[string]bool{}, map[string]bool{}
	providers := map[history.Provider]int{}
	threads := map[history.ThreadKind]int{}
	live, vault, prompts, long := 0, 0, 0, 0
	for _, session := range corpus.Sessions {
		providers[session.Provider]++
		threads[session.ThreadKind]++
		cwds[session.CWD] = true
		project := session.RepositoryName
		if project == "" {
			project = filepath.Base(session.CWD)
		}
		projects[project] = true
		if session.RepositoryName != "" {
			repos[session.RepositoryName] = true
		}
		if session.Live {
			live++
		}
		if session.Vault {
			vault++
		}
		prompts += len(session.Prompts)
		for _, prompt := range session.Prompts {
			if len(prompt.Text) > 4000 {
				long++
			}
		}
	}
	if len(corpus.Sessions) != DefaultSessions || prompts != DefaultPrompts ||
		len(cwds) < 400 || len(repos) < 200 || len(projects) < 400 ||
		providers[history.ProviderCodex] == 0 || providers[history.ProviderClaude] == 0 ||
		threads[history.ThreadRoot] == 0 || threads[history.ThreadSubagent] == 0 || threads[history.ThreadUnknown] == 0 ||
		live != 4950 || vault != 1650 || long == 0 {
		t.Fatalf("default corpus sessions=%d prompts=%d cwd=%d repo=%d project=%d providers=%v threads=%v live=%d vault=%d long=%d",
			len(corpus.Sessions), prompts, len(cwds), len(repos), len(projects), providers, threads, live, vault, long)
	}
}
