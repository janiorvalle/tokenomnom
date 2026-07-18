package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/discover"
)

func newDoctorCommand(codexDir, claudeDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Show discovered coding-agent session data",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find user home directory: %w", err)
			}

			roots, err := discover.Resolve(discover.ResolveOptions{
				CodexDir:  *codexDir,
				ClaudeDir: *claudeDir,
				Home:      home,
				Getenv:    os.Getenv,
			})
			if err != nil {
				return err
			}

			return writeDoctorReport(cmd, roots)
		},
	}
}

func writeDoctorReport(cmd *cobra.Command, roots []discover.Root) error {
	found := make([]discover.Provider, 0, len(roots))
	for index, root := range roots {
		if index > 0 {
			fmt.Fprintln(cmd.OutOrStdout())
		}
		files, walkErrors := discover.ListSourceFiles(root)
		writeProviderReport(cmd, root, files, walkErrors)
		if root.Exists {
			found = append(found, root.Provider)
		}
	}

	fmt.Fprintln(cmd.OutOrStdout())
	switch len(found) {
	case 0:
		fmt.Fprintln(cmd.OutOrStdout(), "Status: no provider data directories found. Use --codex-dir, --claude-dir, or the TOKENOMNOM_*_DIR environment variables to point tokenomnom at them.")
	case 1:
		fmt.Fprintf(cmd.OutOrStdout(), "Status: only %s was found; discovery is ready to use.\n", providerName(found[0]))
	default:
		fmt.Fprintln(cmd.OutOrStdout(), "Status: both providers found; discovery is ready to use.")
	}
	return nil
}

func writeProviderReport(cmd *cobra.Command, root discover.Root, files []discover.SourceFile, walkErrors []error) {
	var totalSize int64
	var oldest time.Time
	var newest time.Time
	for _, file := range files {
		totalSize += file.Size
		if oldest.IsZero() || file.ModTime.Before(oldest) {
			oldest = file.ModTime
		}
		if newest.IsZero() || file.ModTime.After(newest) {
			newest = file.ModTime
		}
	}

	writer := cmd.OutOrStdout()
	fmt.Fprintln(writer, providerName(root.Provider))
	fmt.Fprintf(writer, "  %-12s %s\n", "Path:", root.Path)
	fmt.Fprintf(writer, "  %-12s %s\n", "Source:", root.Source)
	fmt.Fprintf(writer, "  %-12s %s\n", "Exists:", yesNo(root.Exists))
	fmt.Fprintf(writer, "  %-12s %d\n", "JSONL files:", len(files))
	fmt.Fprintf(writer, "  %-12s %s\n", "Total size:", humanBytes(totalSize))
	fmt.Fprintf(writer, "  %-12s %s\n", "Oldest:", formatDate(oldest))
	fmt.Fprintf(writer, "  %-12s %s\n", "Newest:", formatDate(newest))
	if len(walkErrors) == 0 {
		fmt.Fprintf(writer, "  %-12s none\n", "Walk errors:")
		return
	}

	fmt.Fprintf(writer, "  %-12s %d\n", "Walk errors:", len(walkErrors))
	for _, err := range walkErrors {
		fmt.Fprintf(writer, "    - %v\n", err)
	}
}

func providerName(provider discover.Provider) string {
	switch provider {
	case discover.ProviderCodex:
		return "Codex"
	case discover.ProviderClaude:
		return "Claude"
	default:
		return strings.ToUpper(string(provider))
	}
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func formatDate(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Format("2006-01-02")
}

func humanBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}

	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	value := float64(size)
	unit := "B"
	for _, candidate := range units {
		value /= 1024
		unit = candidate
		if value < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}
