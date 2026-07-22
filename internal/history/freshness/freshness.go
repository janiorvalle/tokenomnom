// Package freshness compares live provider metadata with history checkpoints.
package freshness

import (
	"fmt"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
)

// Result is a bounded, content-free view of provider changes since indexing.
type Result struct {
	ChangedSourcesSinceIndex int
	NewSourcesSinceIndex     int
	NewestSourceChange       *time.Time
	AsOf                     time.Time
	Warnings                 []string
}

// Probe stats known provider transcript files and compares them with the
// existing history index without creating, migrating, or writing storage.
func Probe(databasePath string, roots []discover.Root, now func() time.Time) Result {
	if now == nil {
		now = time.Now
	}
	result := Result{AsOf: now(), Warnings: []string{}}
	info, err := historystore.Inspect(databasePath)
	if err != nil || !info.Exists || info.SchemaVersion != historystore.SchemaVersion {
		return result
	}
	database, err := historystore.OpenReadOnly(databasePath)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("inspect history source drift: %v", err))
		return result
	}
	defer database.Close()
	checkpoints, err := database.Checkpoints()
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("inspect history source drift: %v", err))
		return result
	}

	present := make(map[string]bool)
	seenProviders := make(map[history.Provider]bool)
	incompleteProviders := make(map[history.Provider]bool)
	for _, root := range roots {
		if !root.Exists {
			continue
		}
		files, walkErrors := discover.ListSourceFiles(root)
		provider := history.Provider(root.Provider)
		seenProviders[provider] = true
		incompleteProviders[provider] = incompleteProviders[provider] || len(walkErrors) > 0
		for _, walkErr := range walkErrors {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s history freshness: %v", root.Provider, walkErr))
		}
		for _, file := range files {
			key := historystore.CheckpointKey(history.Provider(file.Provider), file.Path)
			present[key] = true
			checkpoint, found := checkpoints[key]
			if found && checkpoint.Size == file.Size && checkpoint.ModTimeUnixNano == file.ModTime.UnixNano() && !checkpoint.Missing {
				continue
			}
			result.ChangedSourcesSinceIndex++
			if !found {
				result.NewSourcesSinceIndex++
			}
			changedAt := file.ModTime
			if result.NewestSourceChange == nil || changedAt.After(*result.NewestSourceChange) {
				result.NewestSourceChange = &changedAt
			}
		}
	}
	for key, checkpoint := range checkpoints {
		if seenProviders[checkpoint.Provider] && !incompleteProviders[checkpoint.Provider] && !checkpoint.Missing && !present[key] {
			result.ChangedSourcesSinceIndex++
		}
	}
	return result
}
