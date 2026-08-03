package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	testRoot, err := os.MkdirTemp("", "tokenomnom-cli-test-")
	if err != nil {
		panic(err)
	}
	for name, value := range map[string]string{
		"TOKENOMNOM_STATE_DIR":  filepath.Join(testRoot, "state"),
		"TOKENOMNOM_DATA_DIR":   filepath.Join(testRoot, "data"),
		"TOKENOMNOM_CONFIG_DIR": filepath.Join(testRoot, "config"),
	} {
		if err := os.Setenv(name, value); err != nil {
			panic(err)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(testRoot)
	os.Exit(code)
}

func isolateTokenomnomDirs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	return root
}
