package cli

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	dataDir, err := os.MkdirTemp("", "tokenomnom-cli-test-data-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("TOKENOMNOM_DATA_DIR", dataDir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dataDir)
	os.Exit(code)
}
