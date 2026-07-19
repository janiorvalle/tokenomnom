package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/backup"
	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

func runDueBackup(cmd *cobra.Command, database *store.Store) error {
	loaded := appconfig.FromContext(cmd.Context())
	if !loaded.Config.Backup.Enabled {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find user home directory: %w", err)
	}
	dir := loaded.Config.Backup.Dir
	if dir == "" {
		dataDir, err := xdg.DataDir(xdg.Options{Home: home, Getenv: os.Getenv})
		if err != nil {
			return err
		}
		dir = filepath.Join(dataDir, "backups")
	} else {
		dir, err = filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("resolve backup directory: %w", err)
		}
	}
	interval, _ := time.ParseDuration(loaded.Config.Backup.Interval)
	_, err = backup.Run(database, backup.Options{Enabled: true, Dir: dir, Interval: interval, Keep: loaded.Config.Backup.Keep})
	return err
}

func backupDir(cmd *cobra.Command) (string, error) {
	loaded := appconfig.FromContext(cmd.Context())
	if loaded.Config.Backup.Dir != "" {
		return filepath.Abs(loaded.Config.Backup.Dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home directory: %w", err)
	}
	dataDir, err := xdg.DataDir(xdg.Options{Home: home, Getenv: os.Getenv})
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "backups"), nil
}
