package main

import (
	"os"

	"github.com/janiorvalle/tokenomnom/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
