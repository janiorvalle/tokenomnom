// Package freshness compares live provider metadata with history checkpoints.
package freshness

import (
	"fmt"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
)

// SettleWindow separates expected churn from running sessions from drift that
// is old enough to warrant another index. It is intentionally not configurable.
const SettleWindow = 10 * time.Minute

// Result is a bounded, content-free view of provider changes since indexing.
type Result struct {
	ChangedSourcesSinceIndex int
	NewSourcesSinceIndex     int
	ActiveChangedSources     int
	ActiveNewSources         int
	SettledChangedSources    int
	SettledNewSources        int
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
	classify := func(changedAt time.Time, isNew bool) {
		result.ChangedSourcesSinceIndex++
		active := activeWithinSettleWindow(changedAt, result.AsOf)
		if active {
			result.ActiveChangedSources++
			if isNew {
				result.ActiveNewSources++
			}
		} else {
			result.SettledChangedSources++
			if isNew {
				result.SettledNewSources++
			}
		}
	}
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
			if !found {
				result.NewSourcesSinceIndex++
			}
			changedAt := file.ModTime
			classify(changedAt, !found)
			if result.NewestSourceChange == nil || changedAt.After(*result.NewestSourceChange) {
				result.NewestSourceChange = &changedAt
			}
		}
	}
	for key, checkpoint := range checkpoints {
		if seenProviders[checkpoint.Provider] && !incompleteProviders[checkpoint.Provider] && !checkpoint.Missing && !present[key] {
			classify(time.Unix(0, checkpoint.ModTimeUnixNano), false)
		}
	}
	return result
}

func activeWithinSettleWindow(changedAt, asOf time.Time) bool {
	return !changedAt.Before(asOf.Add(-SettleWindow)) && !changedAt.After(asOf.Add(SettleWindow))
}
