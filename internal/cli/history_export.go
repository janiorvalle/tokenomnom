package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/history/exporter"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/version"
)

type historyExportOutput struct {
	Path       string   `json:"path"`
	Bytes      int64    `json:"bytes"`
	SessionIDs []string `json:"session_ids"`
}

type historyExportReport struct {
	As                   string                `json:"as"`
	RootSessionID        string                `json:"root_session_id"`
	StructureNonce       string                `json:"structure_nonce"`
	SessionCount         int                   `json:"session_count"`
	TranscriptCount      int                   `json:"transcript_count"`
	Bytes                int64                 `json:"bytes"`
	Outputs              []historyExportOutput `json:"outputs"`
	CollapsedToolRecords int                   `json:"collapsed_tool_records"`
	ExcludedThinking     int                   `json:"excluded_thinking_records"`
	UnrecognizedRecords  int                   `json:"unrecognized_records"`
}

type selectedHistoryExport struct {
	session   historystore.ExportSession
	candidate *historystore.RawCandidate
	raw       []byte
	warning   string
}

type historyRawManifest struct {
	Schema    string                    `json:"schema"`
	CreatedAt string                    `json:"created_at"`
	Root      string                    `json:"root_session_id"`
	Files     []historyRawManifestEntry `json:"files"`
}

type historyRawManifestEntry struct {
	Path      string `json:"path"`
	SessionID string `json:"session_id"`
	Provider  string `json:"provider"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
	Origin    string `json:"origin"`
}

func newHistoryExportCommand(codexDir, claudeDir *string) *cobra.Command {
	var out, as string
	var noSubagents, includeToolOutput, includeThinking, force bool
	command := &cobra.Command{
		Use:   "export <session-id|prompt-id>",
		Short: "Export a full session and its subagents",
		Args:  cobra.ExactArgs(1),
		PreRunE: func(_ *cobra.Command, _ []string) error {
			switch as {
			case "markdown", "raw", "normalized":
			default:
				return fmt.Errorf("invalid --as %q (expected markdown, raw, or normalized)", as)
			}
			if as == "raw" && (includeToolOutput || includeThinking) {
				return errors.New("raw is byte-exact; these flags only affect rendered formats")
			}
			if as == "raw" && out == "" {
				return errors.New("raw history export requires --out; use a directory for a session tree")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			exportedAt := time.Now().UTC()
			var tree []historystore.ExportSession
			candidates := map[string][]historystore.RawCandidate{}
			warnings := []string{}
			if err := withHistoryStore(cmd, func(database *historystore.Store) error {
				var err error
				tree, warnings, err = database.ExportTree(args[0], !noSubagents)
				if err != nil {
					return err
				}
				for _, session := range tree {
					values, candidateErr := database.RawCandidates(session.SessionID, "")
					if candidateErr != nil {
						warnings = append(warnings, candidateErr.Error())
						continue
					}
					candidates[session.SessionID] = values
				}
				return nil
			}); err != nil {
				return err
			}
			if as == "raw" && len(tree) > 1 && !historyExportDirectoryTarget(out) {
				return errors.New("raw history export with subagents requires --out to name a directory (use an existing directory or a trailing slash)")
			}
			selected := make([]selectedHistoryExport, 0, len(tree))
			for _, session := range tree {
				value := selectedHistoryExport{session: session}
				var lastErr error
				for _, candidate := range candidates[session.SessionID] {
					staged, err := readHistoryRawCandidate(cmd, candidate, *codexDir, *claudeDir)
					if err != nil {
						lastErr = err
						warnings = append(warnings, err.Error())
						continue
					}
					content, err := io.ReadAll(staged.file)
					staged.cleanup()
					if err != nil {
						return fmt.Errorf("read validated history export bytes: %w", err)
					}
					copy := candidate
					value.candidate, value.raw = &copy, content
					lastErr = nil
					break
				}
				if value.candidate == nil {
					value.warning = "no retrievable exact transcript content"
					if lastErr != nil {
						value.warning = lastErr.Error()
					} else {
						warnings = append(warnings, fmt.Sprintf("session %s: %s", session.SessionID, value.warning))
					}
				}
				selected = append(selected, value)
			}

			report := historyExportReport{
				As: as, RootSessionID: tree[0].SessionID, SessionCount: len(tree),
				Outputs: []historyExportOutput{},
			}
			for _, value := range selected {
				if value.candidate != nil {
					report.TranscriptCount++
				}
			}
			var err error
			switch as {
			case "raw":
				err = writeRawHistoryExport(out, selected, exportedAt, force, &report)
			default:
				sessions := historyRenderSessions(selected)
				options := exporter.Options{
					IncludeToolOutput: includeToolOutput, IncludeThinking: includeThinking,
					ExportedAt: exportedAt, Version: version.Version,
				}
				if as == "markdown" {
					nonce, nonceErr := exporter.NewStructureNonce()
					if nonceErr != nil {
						return nonceErr
					}
					options.StructureNonce = nonce
					report.StructureNonce = nonce
				}
				err = writeRenderedHistoryExport(cmd, out, as, sessions, options, force, &report)
			}
			if err != nil {
				return err
			}
			if currentFormat(cmd) == "json" {
				return writeHistoryExportJSONReport(cmd, out == "", warnings, report)
			}
			writeHistoryExportPrettyReport(cmd, out == "", warnings, report)
			return nil
		},
	}
	command.Flags().StringVar(&out, "out", "", "write content to a file or directory instead of stdout")
	command.Flags().StringVar(&as, "as", "markdown", "artifact format (markdown, raw, or normalized)")
	command.Flags().BoolVar(&noSubagents, "no-subagents", false, "export only the resolved target session")
	command.Flags().BoolVar(&includeToolOutput, "include-tool-output", false, "include complete tool calls and results")
	command.Flags().BoolVar(&includeThinking, "include-thinking", false, "include thinking and reasoning blocks")
	command.Flags().BoolVar(&force, "force", false, "overwrite existing export files")
	return command
}

func historyRenderSessions(values []selectedHistoryExport) []exporter.Session {
	result := make([]exporter.Session, 0, len(values))
	for _, value := range values {
		session := exporter.Session{
			SessionID: value.session.SessionID, Provider: value.session.Provider,
			NativeSessionID: value.session.NativeSessionID, FirstTimestamp: value.session.FirstTimestamp,
			LastTimestamp: value.session.LastTimestamp, Project: value.session.Project, CWD: value.session.CWD,
			ThreadKind: value.session.ThreadKind, Raw: value.raw, ContentWarning: value.warning,
			ParentNativeMessageID: value.session.ParentNativeMessageID,
		}
		if value.session.ParentSessionID != nil {
			session.ParentSessionID = *value.session.ParentSessionID
		}
		if value.candidate != nil {
			session.SourceSHA256 = value.candidate.ContentSHA256
			session.Origin = value.candidate.Kind
		}
		result = append(result, session)
	}
	return result
}

func writeRenderedHistoryExport(cmd *cobra.Command, out, as string, sessions []exporter.Session, options exporter.Options, force bool, report *historyExportReport) error {
	var destination io.Writer = cmd.OutOrStdout()
	var file *os.File
	var path string
	if out != "" {
		path = out
		if historyExportDirectoryTarget(out) {
			if err := os.MkdirAll(out, 0o700); err != nil {
				return fmt.Errorf("create history export directory: %w", err)
			}
			extension := ".md"
			if as == "normalized" {
				extension = ".jsonl"
			}
			path = filepath.Join(out, historyExportAutoName(string(sessions[0].Provider), sessions[0].FirstTimestamp, sessions[0].SessionID, extension))
		}
		var err error
		file, err = openHistoryExportFile(path, force)
		if err != nil {
			return err
		}
		destination = file
	}
	counter := &countingWriter{writer: destination}
	var counts exporter.Counts
	var err error
	if as == "markdown" {
		counts, err = exporter.RenderMarkdown(counter, sessions, options)
	} else {
		counts, err = exporter.RenderNormalized(counter, sessions, options)
	}
	if err != nil {
		if file != nil {
			_ = file.Close()
			_ = os.Remove(path)
		}
		return err
	}
	if file != nil {
		if err := file.Sync(); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return fmt.Errorf("sync history export %q: %w", path, err)
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(path)
			return fmt.Errorf("close history export %q: %w", path, err)
		}
	}
	sessionIDs := make([]string, 0, len(sessions))
	for _, session := range sessions {
		sessionIDs = append(sessionIDs, session.SessionID)
	}
	outputPath := "stdout"
	if path != "" {
		outputPath = path
	}
	report.Outputs = append(report.Outputs, historyExportOutput{Path: outputPath, Bytes: counter.count, SessionIDs: sessionIDs})
	report.Bytes += counter.count
	report.CollapsedToolRecords = counts.CollapsedToolRecords
	report.ExcludedThinking = counts.ExcludedThinking
	report.UnrecognizedRecords = counts.UnrecognizedRecords
	return nil
}

func writeRawHistoryExport(out string, selected []selectedHistoryExport, exportedAt time.Time, force bool, report *historyExportReport) error {
	if historyExportDirectoryTarget(out) {
		if err := os.MkdirAll(out, 0o700); err != nil {
			return fmt.Errorf("create raw history export directory: %w", err)
		}
		manifest := historyRawManifest{
			Schema: "tokenomnom.history-raw-manifest/v1", CreatedAt: exportedAt.Format(time.RFC3339),
			Root: selected[0].session.SessionID, Files: []historyRawManifestEntry{},
		}
		plannedPaths := []string{filepath.Join(out, "manifest.json")}
		for _, value := range selected {
			if value.candidate != nil {
				plannedPaths = append(plannedPaths, filepath.Join(out, historyExportAutoName(string(value.session.Provider), value.session.FirstTimestamp, value.session.SessionID, ".jsonl")))
			}
		}
		if !force {
			for _, path := range plannedPaths {
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("history export %q already exists; use --force to overwrite", path)
				} else if !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("inspect history export %q: %w", path, err)
				}
			}
		}
		for _, value := range selected {
			if value.candidate == nil {
				continue
			}
			name := historyExportAutoName(string(value.session.Provider), value.session.FirstTimestamp, value.session.SessionID, ".jsonl")
			path := filepath.Join(out, name)
			if err := writeHistoryExportBytes(path, value.raw, force); err != nil {
				return err
			}
			report.Outputs = append(report.Outputs, historyExportOutput{Path: path, Bytes: int64(len(value.raw)), SessionIDs: []string{value.session.SessionID}})
			report.Bytes += int64(len(value.raw))
			manifest.Files = append(manifest.Files, historyRawManifestEntry{
				Path: name, SessionID: value.session.SessionID, Provider: string(value.session.Provider),
				SHA256: value.candidate.ContentSHA256, Bytes: int64(len(value.raw)), Origin: value.candidate.Kind,
			})
		}
		manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
		manifestBytes = append(manifestBytes, '\n')
		manifestPath := filepath.Join(out, "manifest.json")
		if err := writeHistoryExportBytes(manifestPath, manifestBytes, force); err != nil {
			return err
		}
		report.Outputs = append(report.Outputs, historyExportOutput{Path: manifestPath, Bytes: int64(len(manifestBytes)), SessionIDs: []string{}})
		report.Bytes += int64(len(manifestBytes))
		return nil
	}
	if selected[0].candidate == nil {
		return errors.New("target session has no retrievable exact transcript for raw export")
	}
	if err := writeHistoryExportBytes(out, selected[0].raw, force); err != nil {
		return err
	}
	report.Outputs = append(report.Outputs, historyExportOutput{Path: out, Bytes: int64(len(selected[0].raw)), SessionIDs: []string{selected[0].session.SessionID}})
	report.Bytes = int64(len(selected[0].raw))
	return nil
}

func writeHistoryExportBytes(path string, content []byte, force bool) error {
	file, err := openHistoryExportFile(path, force)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(content)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		switch {
		case writeErr != nil:
			return fmt.Errorf("write history export %q: %w", path, writeErr)
		case syncErr != nil:
			return fmt.Errorf("sync history export %q: %w", path, syncErr)
		default:
			return fmt.Errorf("close history export %q: %w", path, closeErr)
		}
	}
	return nil
}

func openHistoryExportFile(path string, force bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create history export parent: %w", err)
	}
	flags := os.O_WRONLY | os.O_CREATE
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("history export %q already exists; use --force to overwrite", path)
	}
	if err != nil {
		return nil, fmt.Errorf("create history export %q: %w", path, err)
	}
	return file, nil
}

func historyExportDirectoryTarget(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err == nil {
		return info.IsDir()
	}
	return strings.HasSuffix(path, "/") || (os.PathSeparator == '\\' && strings.HasSuffix(path, "\\"))
}

func historyExportAutoName(provider string, firstTimestamp *string, sessionID, extension string) string {
	date := "unknown-date"
	if firstTimestamp != nil {
		if parsed, err := time.Parse(time.RFC3339Nano, *firstTimestamp); err == nil {
			date = parsed.UTC().Format("2006-01-02")
		}
	}
	return provider + "-" + date + "-" + sessionID + extension
}

func writeHistoryExportJSONReport(cmd *cobra.Command, contentOnStdout bool, warnings []string, report historyExportReport) error {
	if !contentOnStdout {
		return writeHistoryJSONEnvelope(cmd, "history export", jsonFilters{}, warnings, report)
	}
	original := cmd.OutOrStdout()
	cmd.SetOut(cmd.ErrOrStderr())
	defer cmd.SetOut(original)
	return writeHistoryJSONEnvelope(cmd, "history export", jsonFilters{}, warnings, report)
}

func writeHistoryExportPrettyReport(cmd *cobra.Command, contentOnStdout bool, warnings []string, report historyExportReport) {
	writer := cmd.OutOrStdout()
	if contentOnStdout {
		writer = cmd.ErrOrStderr()
	}
	fmt.Fprintf(writer, "Exported %d session(s), %d transcript(s), %d bytes\n", report.SessionCount, report.TranscriptCount, report.Bytes)
	for _, output := range report.Outputs {
		fmt.Fprintf(writer, "  %s (%d bytes)\n", output.Path, output.Bytes)
	}
	fmt.Fprintf(writer, "Collapsed tool records: %d\nExcluded thinking records: %d\nUnrecognized records: %d\n",
		report.CollapsedToolRecords, report.ExcludedThinking, report.UnrecognizedRecords)
	for _, warning := range warnings {
		fmt.Fprintln(writer, "WARNING: "+warning)
	}
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (writer *countingWriter) Write(value []byte) (int, error) {
	written, err := writer.writer.Write(value)
	writer.count += int64(written)
	return written, err
}
