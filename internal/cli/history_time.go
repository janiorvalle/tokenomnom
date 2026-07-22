package cli

import (
	"time"

	"github.com/spf13/cobra"

	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
)

func historyPresentationTimezone(cmd *cobra.Command) (*time.Location, string) {
	name := requestedTimezone(appconfig.FromContext(cmd.Context()).Config.Sync.Timezone)
	if name == "Local" {
		return time.Local, name
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.Local, localTimezoneName()
	}
	return location, name
}

func writeHistoryJSONEnvelope(cmd *cobra.Command, command string, filters jsonFilters, warnings []string, data any) error {
	_, timezone := historyPresentationTimezone(cmd)
	return writeJSONEnvelope(cmd, command, timezone, filters, warnings, data)
}

func presentHistoryTimestamp(value *string, location *time.Location) *string {
	if value == nil || *value == "" {
		return value
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return value
	}
	formatted := parsed.In(location).Format(time.RFC3339Nano)
	return &formatted
}

func presentHistoryUnix(value int64, location *time.Location) *string {
	if value == 0 {
		return nil
	}
	formatted := time.Unix(value, 0).In(location).Format(time.RFC3339)
	return &formatted
}

func presentHistoryCoverage(value *historystore.QueryCoverage, location *time.Location) {
	value.FirstTimestamp = presentHistoryTimestamp(value.FirstTimestamp, location)
	value.LastTimestamp = presentHistoryTimestamp(value.LastTimestamp, location)
	value.Roles.User.FirstTimestamp = presentHistoryTimestamp(value.Roles.User.FirstTimestamp, location)
	value.Roles.User.LastTimestamp = presentHistoryTimestamp(value.Roles.User.LastTimestamp, location)
	value.Roles.Assistant.FirstTimestamp = presentHistoryTimestamp(value.Roles.Assistant.FirstTimestamp, location)
	value.Roles.Assistant.LastTimestamp = presentHistoryTimestamp(value.Roles.Assistant.LastTimestamp, location)
}

func presentHistoryPrompt(value *historystore.PromptResult, location *time.Location) {
	value.Timestamp = presentHistoryTimestamp(value.Timestamp, location)
}

func presentHistoryPromptPage(value *historystore.PromptsPage, location *time.Location) {
	for index := range value.Prompts {
		presentHistoryPrompt(&value.Prompts[index], location)
	}
	presentHistoryCoverage(&value.Coverage, location)
}

func presentHistorySearchPage(value *historystore.SearchPage, location *time.Location) {
	for index := range value.Hits {
		presentHistoryPrompt(&value.Hits[index], location)
	}
	presentHistoryCoverage(&value.Coverage, location)
}

func presentHistorySession(value *historystore.CatalogSession, location *time.Location) {
	value.FirstTimestamp = presentHistoryTimestamp(value.FirstTimestamp, location)
	value.LastTimestamp = presentHistoryTimestamp(value.LastTimestamp, location)
}

func presentHistoryCatalogPage(value *historystore.CatalogPage, location *time.Location) {
	for index := range value.Sessions {
		presentHistorySession(&value.Sessions[index], location)
	}
}

func presentHistorySample(value *historystore.SampleResult, location *time.Location) {
	for index := range value.Items {
		if value.Items[index].Prompt != nil {
			presentHistoryPrompt(value.Items[index].Prompt, location)
		}
		if value.Items[index].Session != nil {
			presentHistorySession(value.Items[index].Session, location)
		}
	}
	value.Coverage.FirstTimestamp = presentHistoryTimestamp(value.Coverage.FirstTimestamp, location)
	value.Coverage.LastTimestamp = presentHistoryTimestamp(value.Coverage.LastTimestamp, location)
}

func presentHistoryStatistics(value *historystore.Statistics, location *time.Location) {
	presentHistoryCoverage(&value.Coverage, location)
}
