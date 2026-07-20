package cli

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/backup"
	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/schedule"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

var (
	schedulePlatform                   = runtime.GOOS
	scheduleExecutable                 = os.Executable
	scheduleUserHome                   = os.UserHomeDir
	scheduleRunner     schedule.Runner = schedule.ExecRunner{}
)

type jsonScheduleData struct {
	schedule.Status
	LastSync      *string `json:"last_sync"`
	LastBackup    *string `json:"last_backup"`
	LastAutoVault *string `json:"last_auto_vault"`
	Uninstalled   bool    `json:"uninstalled,omitempty"`
}

func newScheduleCommand() *cobra.Command {
	command := &cobra.Command{Use: "schedule", Short: "Manage the per-user maintenance schedule", Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{
		Use: "install", Short: "Install or refresh the per-user maintenance schedule", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options, err := currentScheduleOptions(cmd)
			if err != nil {
				return err
			}
			status, err := schedule.Install(options)
			if err != nil {
				return err
			}
			data, err := scheduleData(cmd, status)
			if err != nil {
				return err
			}
			if currentFormat(cmd) == "json" {
				return writeJSONEnvelope(cmd, "schedule install", requestedTimezone(""), jsonFilters{}, nil, data)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Installed %s schedule at %s\n", status.Mechanism, status.UnitPath)
			fmt.Fprintf(cmd.OutOrStdout(), "Runs %s sync --scheduled every %s\n", status.BinaryPath, status.ConfiguredInterval)
			fmt.Fprintln(cmd.OutOrStdout(), "Re-run schedule install after moving or upgrading the binary, or after changing schedule.interval.")
			return nil
		},
	})
	command.AddCommand(&cobra.Command{
		Use: "status", Short: "Show the per-user maintenance schedule", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options, err := currentScheduleOptions(cmd)
			if err != nil {
				return err
			}
			status, err := schedule.Inspect(options)
			if err != nil {
				return err
			}
			data, err := scheduleData(cmd, status)
			if err != nil {
				return err
			}
			if currentFormat(cmd) == "json" {
				return writeJSONEnvelope(cmd, "schedule status", requestedTimezone(""), jsonFilters{}, nil, data)
			}
			writeScheduleStatus(cmd, data)
			return nil
		},
	})
	command.AddCommand(&cobra.Command{
		Use: "uninstall", Short: "Remove the per-user maintenance schedule", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options, err := currentScheduleOptions(cmd)
			if err != nil {
				return err
			}
			definition, err := schedule.Locate(options)
			if err != nil {
				return err
			}
			if err := schedule.Uninstall(options); err != nil {
				return err
			}
			after, err := schedule.Inspect(options)
			if err != nil {
				return err
			}
			data, err := scheduleData(cmd, after)
			if err != nil {
				return err
			}
			data.Uninstalled = true
			if currentFormat(cmd) == "json" {
				return writeJSONEnvelope(cmd, "schedule uninstall", requestedTimezone(""), jsonFilters{}, nil, data)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s schedule from %s\n", definition.Mechanism, definition.UnitPath)
			return nil
		},
	})
	return command
}

func currentScheduleOptions(cmd *cobra.Command) (schedule.Options, error) {
	home, err := scheduleUserHome()
	if err != nil {
		return schedule.Options{}, fmt.Errorf("find user home directory: %w", err)
	}
	executable, err := scheduleExecutable()
	if err != nil {
		return schedule.Options{}, fmt.Errorf("find current executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return schedule.Options{}, fmt.Errorf("resolve current executable: %w", err)
	}
	interval, _ := time.ParseDuration(appconfig.FromContext(cmd.Context()).Config.Schedule.Interval)
	configDir := ""
	if schedulePlatform == "windows" {
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		configDir, err = filepath.Abs(filepath.Join(base, "tokenomnom"))
		if err != nil {
			return schedule.Options{}, fmt.Errorf("resolve Windows scheduler data directory: %w", err)
		}
	}
	uid := ""
	if current, userErr := user.Current(); userErr == nil {
		uid = current.Uid
	}
	return schedule.Options{GOOS: schedulePlatform, Home: home, ConfigDir: configDir, UID: uid, Executable: executable, Interval: interval, Runner: scheduleRunner}, nil
}

func scheduleData(cmd *cobra.Command, status schedule.Status) (jsonScheduleData, error) {
	result := jsonScheduleData{Status: status}
	result.ConfiguredInterval = appconfig.FromContext(cmd.Context()).Config.Schedule.Interval
	home, err := scheduleUserHome()
	if err != nil {
		return result, err
	}
	stateDir, err := xdg.StateDir(xdg.Options{Home: home, Getenv: os.Getenv})
	if err != nil {
		return result, err
	}
	databasePath := filepath.Join(stateDir, store.DatabaseName)
	if _, err := os.Stat(databasePath); errorsIsNotExist(err) {
		return result, nil
	} else if err != nil {
		return result, err
	}
	database, err := store.Open(databasePath)
	if err != nil {
		return result, err
	}
	defer database.Close()
	for key, target := range map[string]**string{
		"last_sync_unix":  &result.LastSync,
		backup.MetaKey:    &result.LastBackup,
		lastAutoVaultMeta: &result.LastAutoVault,
	} {
		value, err := database.Meta(key)
		if err != nil {
			return result, err
		}
		if value == "" {
			continue
		}
		unix, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return result, fmt.Errorf("parse %s: %w", key, err)
		}
		formatted := time.Unix(unix, 0).Format(time.RFC3339)
		*target = &formatted
	}
	return result, nil
}

func errorsIsNotExist(err error) bool { return err != nil && os.IsNotExist(err) }

func writeScheduleStatus(cmd *cobra.Command, data jsonScheduleData) {
	writeHeading(cmd, "Schedule")
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Mechanism:", data.Mechanism)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Installed:", yesNo(data.Installed))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Definition exists:", yesNo(data.DefinitionExists))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Configured interval:", data.ConfiguredInterval)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Installed interval:", stringValue(data.InstalledIntervalText))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Unit/task path:", data.UnitPath)
	if data.TaskName != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Task name:", data.TaskName)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Binary path:", dashIfEmpty(data.BinaryPath))
	binary := "-"
	if data.BinaryPath != "" {
		binary = yesNo(data.BinaryExists)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Binary exists:", binary)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Interval drift:", yesNo(data.IntervalDrift))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Last sync:", stringValue(data.LastSync))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Last backup:", stringValue(data.LastBackup))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Last auto-vault:", stringValue(data.LastAutoVault))
}
