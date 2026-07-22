package history

import "testing"

func TestDeriveRepository(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		identity string
		repo     string
	}{
		{name: "https", raw: "https://github.com/OpenAI/Codex.git", identity: "github.com/OpenAI/Codex", repo: "Codex"},
		{name: "https credentials and trailing slash", raw: "https://build-user:secret@git.example.com/Unusual.Org/Repo.Name.git/", identity: "git.example.com/Unusual.Org/Repo.Name", repo: "Repo.Name"},
		{name: "scp ssh", raw: "git@github.com:Acme-Co/TokenOmNom.git", identity: "github.com:Acme-Co/TokenOmNom", repo: "TokenOmNom"},
		{name: "ssh URL", raw: "ssh://git@github.com/Acme/Repo.git/", identity: "github.com/Acme/Repo", repo: "Repo"},
		{name: "trailing slash", raw: "https://code.example/Org_With.Mixed-Names/project/", identity: "code.example/Org_With.Mixed-Names/project", repo: "project"},
		{name: "scheme-less", raw: "deploy@code.example:odd+org/repository.GIT", identity: "code.example:odd+org/repository", repo: "repository"},
		{name: "bare name", raw: "repo", identity: "repo", repo: "repo"},
		{name: "empty", raw: "  ", identity: "", repo: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identity, repo := DeriveRepository(test.raw)
			if identity != test.identity || repo != test.repo {
				t.Fatalf("DeriveRepository(%q) = %q, %q; want %q, %q", test.raw, identity, repo, test.identity, test.repo)
			}
		})
	}
}
