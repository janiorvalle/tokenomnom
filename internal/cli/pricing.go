package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	pricinglib "github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

const pricingDisclaimer = "Dollar figures are API list-price equivalents, not actual bills."

func newPricingCommand(timezone *string) *cobra.Command {
	return &cobra.Command{
		Use:   "pricing",
		Short: "Show effective API list rates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			table, err := loadPricingTable()
			if err != nil {
				return err
			}
			if currentFormat(cmd) == "json" {
				return writePricingJSON(cmd, table, requestedTimezone(*timezone))
			}
			return writePricingTable(cmd, table)
		},
	}
}

type jsonPricingEntry struct {
	BaseInput      *float64 `json:"base_input"`
	CacheRead      *float64 `json:"cache_read"`
	Write5m        *float64 `json:"write_5m"`
	Write1h        *float64 `json:"write_1h"`
	Output         *float64 `json:"output"`
	Status         string   `json:"status"`
	EffectiveFrom  *string  `json:"effective_from"`
	EffectiveUntil *string  `json:"effective_until"`
	Source         string   `json:"source"`
	Notes          string   `json:"notes"`
	Overridden     bool     `json:"overridden"`
}

type jsonPricingModel struct {
	Model   string             `json:"model"`
	Entries []jsonPricingEntry `json:"entries"`
}

type jsonPricingData struct {
	Models []jsonPricingModel `json:"models"`
}

func writePricingJSON(cmd *cobra.Command, table pricinglib.Table, timezone string) error {
	models := []jsonPricingModel{}
	for _, entry := range table.Entries() {
		if len(models) == 0 || models[len(models)-1].Model != entry.Model {
			models = append(models, jsonPricingModel{Model: entry.Model, Entries: []jsonPricingEntry{}})
		}
		models[len(models)-1].Entries = append(models[len(models)-1].Entries, jsonPricingEntry{
			BaseInput: rateJSON(entry.BaseInput), CacheRead: rateJSON(entry.CacheRead),
			Write5m: rateJSON(entry.Write5m), Write1h: rateJSON(entry.Write1h), Output: rateJSON(entry.Output),
			Status: entry.Status, EffectiveFrom: optionalString(entry.EffectiveFrom), EffectiveUntil: optionalString(entry.EffectiveUntil),
			Source: entry.Source, Notes: entry.Notes, Overridden: table.IsOverridden(entry.Model),
		})
	}
	return writeJSONEnvelope(cmd, "pricing", timezone, jsonFilters{}, nil, jsonPricingData{Models: models})
}

func rateJSON(rate *pricinglib.Rate) *float64 {
	if rate == nil {
		return nil
	}
	value := float64(*rate) / 1000
	return &value
}

func loadPricingTable() (pricinglib.Table, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return pricinglib.Table{}, fmt.Errorf("find user home directory: %w", err)
	}
	configDir, err := xdg.ConfigDir(xdg.Options{Home: home, Getenv: os.Getenv})
	if err != nil {
		return pricinglib.Table{}, err
	}
	path := filepath.Join(configDir, "pricing.json")
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return pricinglib.Load()
	}
	if err != nil {
		return pricinglib.Table{}, fmt.Errorf("open pricing override %q: %w", path, err)
	}
	defer file.Close()
	table, err := pricinglib.Load(file)
	if err != nil {
		return pricinglib.Table{}, fmt.Errorf("load pricing override %q: %w", path, err)
	}
	return table, nil
}

func writePricingTable(cmd *cobra.Command, table pricinglib.Table) error {
	rows := make([][]string, 0, len(table.Entries()))
	for _, entry := range table.Entries() {
		override := "—"
		if table.IsOverridden(entry.Model) {
			override = "yes"
		}
		rows = append(rows, []string{
			entry.Model,
			pricinglib.FormatRate(entry.BaseInput),
			pricinglib.FormatRate(entry.CacheRead),
			pricinglib.FormatRate(entry.Write5m),
			pricinglib.FormatRate(entry.Write1h),
			pricinglib.FormatRate(entry.Output),
			entry.Status,
			effectiveWindow(entry),
			entry.Source,
			override,
		})
	}
	writeReportTable(cmd,
		[]string{"MODEL", "BASE INPUT", "CACHE READ", "WRITE 5M", "WRITE 1H", "OUTPUT", "STATUS", "EFFECTIVE", "SOURCE", "OVERRIDE"},
		rows, []bool{false, true, true, true, true, true, false, false, false, false},
		tableStyle{hasModel: true, modelCol: 0, moneyColumns: map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true}, badgeColumns: map[int]bool{6: true, 9: true}},
	)
	writeSubtleLine(cmd, pricingDisclaimer)
	return nil
}

func effectiveWindow(entry pricinglib.Entry) string {
	switch {
	case entry.EffectiveFrom == "" && entry.EffectiveUntil == "":
		return "always"
	case entry.EffectiveFrom == "":
		return "through " + entry.EffectiveUntil
	case entry.EffectiveUntil == "":
		return "from " + entry.EffectiveFrom
	default:
		return entry.EffectiveFrom + " to " + entry.EffectiveUntil
	}
}
