package history

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeriveProject(t *testing.T) {
	for _, test := range []struct {
		name, repository, cwd, project string
		source                         ProjectSource
	}{
		{"git wins", "tokenomnom", "/workspace/fallback", "tokenomnom", ProjectSourceGit},
		{"posix cwd", "", "/workspace/demo/", "demo", ProjectSourceCWD},
		{"windows cwd", "", `C:\workspace\demo`, "demo", ProjectSourceCWD},
		{"windows drive root", "", `C:\`, "unknown", ProjectSourceUnknown},
		{"git wins over temp cwd", "tokenomnom", "/tmp/task", "tokenomnom", ProjectSourceGit},
		{"posix tmp", "", "/tmp/task", "unknown", ProjectSourceUnknown},
		{"posix private tmp", "", "/private/tmp/task", "unknown", ProjectSourceUnknown},
		{"darwin per-user tmp", "", "/var/folders/ab/cdef/T/task", "unknown", ProjectSourceUnknown},
		{"posix temp prefix boundary", "", "/tmp-project/demo", "demo", ProjectSourceCWD},
		{"windows user temp", "", `C:\Users\demo\AppData\Local\Temp\task`, "unknown", ProjectSourceUnknown},
		{"windows temp case insensitive", "", `c:\WINDOWS\Temp\task`, "unknown", ProjectSourceUnknown},
		{"windows system temp", "", `D:\Windows\SystemTemp\task`, "unknown", ProjectSourceUnknown},
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

func TestDeriveProjectUsesRuntimeTempDir(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "custom-temp"))
	cwd := filepath.Join(os.TempDir(), "task")
	project, source := DeriveProject("", cwd)
	if project != "unknown" || source != ProjectSourceUnknown {
		t.Fatalf("DeriveProject(%q) = %q, %q", cwd, project, source)
	}
}
