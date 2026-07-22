package history

import "testing"

func TestDeriveProject(t *testing.T) {
	for _, test := range []struct {
		name, repository, cwd, project string
		source                         ProjectSource
	}{
		{"git wins", "tokenomnom", "/workspace/fallback", "tokenomnom", ProjectSourceGit},
		{"posix cwd", "", "/workspace/demo/", "demo", ProjectSourceCWD},
		{"windows cwd", "", `C:\workspace\demo`, "demo", ProjectSourceCWD},
		{"windows drive root", "", `C:\`, "unknown", ProjectSourceUnknown},
		{"unknown", "", "/", "unknown", ProjectSourceUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, source := DeriveProject(test.repository, test.cwd)
			if project != test.project || source != test.source {
				t.Fatalf("DeriveProject(%q, %q) = %q, %q; want %q, %q", test.repository, test.cwd, project, source, test.project, test.source)
			}
		})
	}
}
