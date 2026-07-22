package history

import "strings"

// ProjectSource names the evidence used for a derived project label.
type ProjectSource string

const (
	ProjectSourceGit     ProjectSource = "git"
	ProjectSourceCWD     ProjectSource = "cwd"
	ProjectSourceUnknown ProjectSource = "unknown"
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
