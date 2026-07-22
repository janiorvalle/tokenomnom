package history

import (
	"os"
	"strings"
)

// ProjectSource names the evidence used for a derived project label.
type ProjectSource string

const (
	ProjectSourceGit     ProjectSource = "git"
	ProjectSourceCWD     ProjectSource = "cwd"
	ProjectSourceUnknown ProjectSource = "unknown"
	// ProjectGroupMinSessions folds one-off project labels into an explicit
	// presentation-only remainder for grouped statistics and sampling.
	ProjectGroupMinSessions = 2
)

// DeriveProject returns a cross-provider presentation label without changing
// the stricter, git-proven repository fields.
func DeriveProject(repositoryName, cwd string) (string, ProjectSource) {
	if repositoryName = strings.TrimSpace(repositoryName); repositoryName != "" {
		return repositoryName, ProjectSourceGit
	}
	path := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(cwd), `\`, "/"), "/")
	if path == "" {
		return "unknown", ProjectSourceUnknown
	}
	if ephemeralCWD(path) {
		return "unknown", ProjectSourceUnknown
	}
	if len(path) == 2 && path[1] == ':' && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) {
		return "unknown", ProjectSourceUnknown
	}
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		path = path[index+1:]
	}
	if path == "" {
		return "unknown", ProjectSourceUnknown
	}
	return path, ProjectSourceCWD
}

func ephemeralCWD(path string) bool {
	for _, root := range []string{os.TempDir(), "/private/tmp", "/tmp", "/var/folders"} {
		if pathWithin(path, root, false) {
			return true
		}
	}
	normalized := strings.ToLower(strings.TrimRight(strings.ReplaceAll(path, `\`, "/"), "/"))
	parts := strings.Split(normalized, "/")
	if len(parts) >= 3 && windowsVolume(parts[0]) && parts[1] == "windows" && (parts[2] == "temp" || parts[2] == "systemtemp") {
		return true
	}
	return len(parts) >= 6 && windowsVolume(parts[0]) && parts[1] == "users" && parts[3] == "appdata" && parts[4] == "local" && parts[5] == "temp"
}

func pathWithin(path, root string, foldCase bool) bool {
	path = strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(path), `\`, "/"), "/")
	root = strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(root), `\`, "/"), "/")
	if path == "" || root == "" || root == "." {
		return false
	}
	if foldCase || hasWindowsVolume(path) || hasWindowsVolume(root) || strings.HasPrefix(path, "//") || strings.HasPrefix(root, "//") {
		path, root = strings.ToLower(path), strings.ToLower(root)
	}
	return path == root || strings.HasPrefix(path, root+"/")
}

func windowsVolume(path string) bool {
	return len(path) == 2 && path[1] == ':' && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z'))
}

func hasWindowsVolume(path string) bool {
	return len(path) >= 2 && windowsVolume(path[:2])
}
