package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	pricinglib "github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

const pricingDisclaimer = "Dollar figures are API list-price equivalents; user rate figures are estimates, not actual bills."

const userRatesFileName = "user-rates.json"

func newPricingCommand(timezone *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "pricing",
		Short: "Show effective API and user rates",
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
	command.AddCommand(newPricingSetRateCommand(timezone))
	return command
}

type pricingCommandError struct {
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	Example     string   `json:"example"`
	Suggestions []string `json:"suggestions,omitempty"`
}

type pricingSetRateOptions struct {
	Model      string
	Input      string
	Output     string
	CacheRead  string
	CacheWrite string
	Clear      bool
	Help       bool
}

func (err pricingCommandError) Error() string {
	message := fmt.Sprintf("%s (code %s)", err.Message, err.Code)
	if len(err.Suggestions) > 0 {
		message += "; known models: " + strings.Join(err.Suggestions, ", ")
	}
	if err.Example != "" {
		message += "; try: " + err.Example
	}
	return message
}

func newPricingSetRateCommand(timezone *string) *cobra.Command {
	command := &cobra.Command{
		Use:                "set-rate <model>",
		Short:              "Set or clear a user rate estimate for one model",
		DisableFlagParsing: true,
		SilenceErrors:      true,
		SilenceUsage:       true,
		Args:               func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			options, err := parsePricingSetRateArgs(args)
			if err != nil {
				return reportPricingCommandError(cmd, timezone, err.(pricingCommandError))
			}
			if options.Help {
				return cmd.Help()
			}
			model := options.Model
			if strings.TrimSpace(model) == "" {
				return reportPricingCommandError(cmd, timezone, pricingCommandError{Code: "INVALID_MODEL", Message: "model name must not be empty", Example: "tokenomnom pricing set-rate gpt-5.6-terra --input 1 --output 5"})
			}
			path, err := userRatesPath()
			if err != nil {
				return reportPricingCommandError(cmd, timezone, pricingCommandError{Code: "CONFIG_PATH", Message: err.Error()})
			}
			release, err := lockUserRates(path)
			if err != nil {
				return reportPricingCommandError(cmd, timezone, pricingCommandError{Code: "USER_RATES_LOCK_FAILED", Message: err.Error(), Example: "wait for another pricing update to finish, then retry"})
			}
			defer release()
			rates, err := readUserRates(path)
			if err != nil {
				return reportPricingCommandError(cmd, timezone, pricingCommandError{Code: "USER_RATES_INVALID", Message: err.Error(), Example: "remove or repair " + path})
			}
			if options.Clear {
				if options.Input != "" || options.Output != "" || options.CacheRead != "" || options.CacheWrite != "" {
					return reportPricingCommandError(cmd, timezone, pricingCommandError{Code: "INVALID_FLAGS", Message: "--clear cannot be combined with rate flags", Example: "tokenomnom pricing set-rate " + model + " --clear"})
				}
				_, existed := rates[model]
				delete(rates, model)
				if err := writeUserRates(path, rates); err != nil {
					return reportPricingCommandError(cmd, timezone, pricingCommandError{Code: "USER_RATES_WRITE_FAILED", Message: err.Error(), Example: "retry tokenomnom pricing set-rate " + model + " --clear"})
				}
				return writePricingMutation(cmd, timezone, pricingMutation{Action: "clear", Model: model, Path: path, Changed: existed})
			}

			if options.Input == "" || options.Output == "" {
				return reportPricingCommandError(cmd, timezone, pricingCommandError{Code: "MISSING_RATE", Message: "--input and --output are required unless --clear is used", Example: "tokenomnom pricing set-rate " + model + " --input 1 --output 5"})
			}
			baseTable, err := loadPricingTable()
			if err != nil {
				return reportPricingCommandError(cmd, timezone, pricingCommandError{Code: "PRICING_LOAD_FAILED", Message: err.Error(), Example: "repair the pricing files, then retry"})
			}
			if err := validateUserRateModel(model, baseTable, rates); err != nil {
				return reportPricingCommandError(cmd, timezone, err.(pricingCommandError))
			}
			rate, err := parseUserRateFlags(options.Input, options.Output, options.CacheRead, options.CacheWrite, model)
			if err != nil {
				return reportPricingCommandError(cmd, timezone, err.(pricingCommandError))
			}
			rates[model] = rate
			if err := writeUserRates(path, rates); err != nil {
				return reportPricingCommandError(cmd, timezone, pricingCommandError{Code: "USER_RATES_WRITE_FAILED", Message: err.Error(), Example: "retry tokenomnom pricing set-rate " + model + " --input 1 --output 5"})
			}
			return writePricingMutation(cmd, timezone, pricingMutation{Action: "set", Model: model, Path: path, Changed: true, Rate: rate})
		},
	}
	command.Flags().String("input", "", "user USD-per-million input rate")
	command.Flags().String("output", "", "user USD-per-million output rate")
	command.Flags().String("cache-read", "", "optional user USD-per-million cache-read rate")
	command.Flags().String("cache-write", "", "optional user USD-per-million cache-write rate")
	command.Flags().Bool("clear", false, "remove the saved user rate")
	return command
}

func parsePricingSetRateArgs(args []string) (pricingSetRateOptions, error) {
	var options pricingSetRateOptions
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			positionals = append(positionals, args[index+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}
		if arg == "-h" || arg == "--help" {
			options.Help = true
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") {
			return pricingSetRateOptions{}, pricingCommandError{Code: "INVALID_FLAGS", Message: fmt.Sprintf("unsupported flag %q", arg), Example: "tokenomnom pricing set-rate gpt-5.6-terra --input 1 --output 5 --format json"}
		}
		switch name {
		case "clear":
			if hasValue {
				return pricingSetRateOptions{}, invalidPricingSetRateFlag(arg)
			}
			options.Clear = true
		case "input", "output", "cache-read", "cache-write", "format", "tz", "codex-dir", "claude-dir":
			if !hasValue {
				if index+1 >= len(args) {
					return pricingSetRateOptions{}, invalidPricingSetRateFlag(arg + " requires a value")
				}
				index++
				value = args[index]
			}
			switch name {
			case "input":
				options.Input = value
			case "output":
				options.Output = value
			case "cache-read":
				options.CacheRead = value
			case "cache-write":
				options.CacheWrite = value
			}
		case "no-color":
			if hasValue {
				return pricingSetRateOptions{}, invalidPricingSetRateFlag(arg)
			}
		default:
			return pricingSetRateOptions{}, invalidPricingSetRateFlag(arg)
		}
	}
	if options.Help {
		return options, nil
	}
	if len(positionals) == 0 {
		return pricingSetRateOptions{}, pricingCommandError{Code: "MISSING_MODEL", Message: "exactly one model name is required", Example: "tokenomnom pricing set-rate gpt-5.6-terra --input 1 --output 5"}
	}
	if len(positionals) > 1 {
		return pricingSetRateOptions{}, pricingCommandError{Code: "INVALID_ARGUMENTS", Message: "set-rate accepts exactly one model name", Example: "tokenomnom pricing set-rate gpt-5.6-terra --input 1 --output 5"}
	}
	options.Model = positionals[0]
	return options, nil
}

func invalidPricingSetRateFlag(value string) pricingCommandError {
	return pricingCommandError{Code: "INVALID_FLAGS", Message: value, Example: "tokenomnom pricing set-rate gpt-5.6-terra --input 1 --output 5 --format json"}
}

func preparePricingSetRateFlags(cmd *cobra.Command, args []string) error {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if name != "format" && name != "tz" && name != "codex-dir" && name != "claude-dir" && name != "no-color" {
			continue
		}
		if !hasValue && name != "no-color" {
			if index+1 >= len(args) {
				continue
			}
			index++
			value = args[index]
		}
		if err := cmd.Flags().Set(name, valueOrBool(value, name == "no-color")); err != nil {
			return err
		}
	}
	return nil
}

func valueOrBool(value string, boolean bool) string {
	if boolean {
		return "true"
	}
	return value
}

type pricingMutation struct {
	Action  string
	Model   string
	Path    string
	Changed bool
	Rate    pricinglib.UserRate
}

type jsonPricingMutation struct {
	Action   string        `json:"action"`
	Model    string        `json:"model"`
	Path     string        `json:"path"`
	Changed  bool          `json:"changed"`
	UserRate *jsonUserRate `json:"user_rate,omitempty"`
}

type jsonUserRate struct {
	Input      *float64 `json:"input"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
	Output     *float64 `json:"output"`
}

func writePricingMutation(cmd *cobra.Command, timezone *string, mutation pricingMutation) error {
	if currentFormat(cmd) == "json" {
		var rate *jsonUserRate
		if mutation.Action == "set" {
			rate = &jsonUserRate{Input: rateJSON(mutation.Rate.Input), CacheRead: rateJSON(mutation.Rate.CacheRead), CacheWrite: rateJSON(mutation.Rate.CacheWrite), Output: rateJSON(mutation.Rate.Output)}
		}
		return writeJSONEnvelope(cmd, "pricing set-rate", requestedTimezone(*timezone), jsonFilters{}, nil, jsonPricingMutation{
			Action: mutation.Action, Model: mutation.Model, Path: mutation.Path, Changed: mutation.Changed, UserRate: rate,
		})
	}
	if mutation.Action == "clear" {
		if mutation.Changed {
			fmt.Fprintf(cmd.OutOrStdout(), "Cleared user rate for %s.\n", mutation.Model)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "No user rate was saved for %s.\n", mutation.Model)
		}
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Set user rate for %s: input %s, output %s", mutation.Model, pricinglib.FormatRate(mutation.Rate.Input), pricinglib.FormatRate(mutation.Rate.Output))
	if mutation.Rate.CacheRead != nil {
		fmt.Fprintf(cmd.OutOrStdout(), ", cache read %s", pricinglib.FormatRate(mutation.Rate.CacheRead))
	}
	if mutation.Rate.CacheWrite != nil {
		fmt.Fprintf(cmd.OutOrStdout(), ", cache write %s", pricinglib.FormatRate(mutation.Rate.CacheWrite))
	}
	fmt.Fprintf(cmd.OutOrStdout(), ".\nSaved to %s. User rate figures are estimates.\n", mutation.Path)
	return nil
}

func reportPricingCommandError(cmd *cobra.Command, timezone *string, value pricingCommandError) error {
	if currentFormat(cmd) == "json" {
		return reportPricingCommandErrorJSON(cmd, timezone, value)
	}
	fmt.Fprintln(cmd.ErrOrStderr(), value.Error())
	return value
}

func reportPricingCommandErrorJSON(cmd *cobra.Command, timezone *string, value pricingCommandError) error {
	command := strings.TrimPrefix(cmd.CommandPath(), "tokenomnom ")
	if err := writeJSONEnvelope(cmd, command, requestedTimezone(*timezone), jsonFilters{}, nil, struct {
		Error pricingCommandError `json:"error"`
	}{Error: value}); err != nil {
		return err
	}
	return value
}

func parseUserRateFlags(input, output, cacheRead, cacheWrite, model string) (pricinglib.UserRate, error) {
	parse := func(name, value string, required bool) (*pricinglib.Rate, error) {
		if value == "" && !required {
			return nil, nil
		}
		rate, err := pricinglib.ParseRate(value)
		if err != nil {
			return nil, pricingCommandError{Code: "INVALID_RATE", Message: fmt.Sprintf("%s rate %q is invalid: %v", name, value, err), Example: "tokenomnom pricing set-rate " + model + " --input 1 --output 5"}
		}
		return &rate, nil
	}
	inputRate, err := parse("--input", input, true)
	if err != nil {
		return pricinglib.UserRate{}, err
	}
	outputRate, err := parse("--output", output, true)
	if err != nil {
		return pricinglib.UserRate{}, err
	}
	cacheReadRate, err := parse("--cache-read", cacheRead, false)
	if err != nil {
		return pricinglib.UserRate{}, err
	}
	cacheWriteRate, err := parse("--cache-write", cacheWrite, false)
	if err != nil {
		return pricinglib.UserRate{}, err
	}
	return pricinglib.UserRate{Input: inputRate, Output: outputRate, CacheRead: cacheReadRate, CacheWrite: cacheWriteRate}, nil
}

func validateUserRateModel(model string, table pricinglib.Table, rates map[string]pricinglib.UserRate) error {
	known := make(map[string]bool)
	for _, value := range table.Models() {
		known[value] = true
	}
	for value := range rates {
		known[value] = true
	}
	for _, value := range storedModelNames() {
		known[value] = true
	}
	if known[model] {
		return nil
	}
	suggestions := closestModelNames(model, sortedKeys(known), 2, 3)
	if len(suggestions) == 0 {
		// A model absent from the published table is precisely the primary use
		// case for this command. Reject likely typos, but allow a genuinely new
		// observed model name to be recorded before the next sync.
		return nil
	}
	return pricingCommandError{
		Code:        "UNKNOWN_MODEL",
		Message:     fmt.Sprintf("model %q is not in the known model set", model),
		Example:     "tokenomnom pricing set-rate " + suggestions[0] + " --input 1 --output 5",
		Suggestions: suggestions,
	}
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func closestModelNames(model string, known []string, maxDistance, limit int) []string {
	type candidate struct {
		name     string
		distance int
	}
	values := make([]candidate, 0, len(known))
	for _, value := range known {
		distance := levenshtein(strings.ToLower(model), strings.ToLower(value))
		if distance <= maxDistance {
			values = append(values, candidate{name: value, distance: distance})
		}
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].distance != values[right].distance {
			return values[left].distance < values[right].distance
		}
		return values[left].name < values[right].name
	})
	if len(values) > limit {
		values = values[:limit]
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.name)
	}
	return result
}

func levenshtein(left, right string) int {
	previous := make([]int, len(right)+1)
	for index := range previous {
		previous[index] = index
	}
	for leftIndex, leftRune := range left {
		current := make([]int, len(right)+1)
		current[0] = leftIndex + 1
		for rightIndex, rightRune := range right {
			cost := 0
			if leftRune != rightRune {
				cost = 1
			}
			current[rightIndex+1] = minInt(current[rightIndex]+1, previous[rightIndex+1]+1, previous[rightIndex]+cost)
		}
		previous = current
	}
	return previous[len(right)]
}

func minInt(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func storedModelNames() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	stateDir, err := xdg.StateDir(xdg.Options{Home: home, Getenv: os.Getenv})
	if err != nil {
		return nil
	}
	database, err := store.OpenReadOnly(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		return nil
	}
	defer database.Close()
	rows, err := database.ByModel(store.Filter{})
	if err != nil {
		return nil
	}
	models := make([]string, 0, len(rows))
	for _, row := range rows {
		models = append(models, row.Model)
	}
	return models
}

func userRatesPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home directory: %w", err)
	}
	configDir, err := xdg.ConfigDir(xdg.Options{Home: home, Getenv: os.Getenv})
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, userRatesFileName), nil
}

func readUserRates(path string) (map[string]pricinglib.UserRate, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]pricinglib.UserRate{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open user rates %q: %w", path, err)
	}
	defer file.Close()
	rates, err := pricinglib.LoadUserRates(file)
	if err != nil {
		return nil, fmt.Errorf("load user rates %q: %w", path, err)
	}
	return rates, nil
}

func writeUserRates(path string, rates map[string]pricinglib.UserRate) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create user rates directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("secure user rates directory: %w", err)
	}
	if err := atomicWrite(path, func(writer io.Writer) error { return pricinglib.WriteUserRates(writer, rates) }); err != nil {
		return fmt.Errorf("write user rates %q: %w", path, err)
	}
	return nil
}

func lockUserRates(path string) (func(), error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create user rates directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("secure user rates directory: %w", err)
	}
	return store.LockPath(path + ".lock")
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
	Provenance     string   `json:"provenance"`
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
			Source: entry.Source, Notes: entry.Notes, Overridden: table.IsOverridden(entry.Model), Provenance: entry.Status,
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
	override, err := os.Open(path)
	if err != nil && !os.IsNotExist(err) {
		return pricinglib.Table{}, fmt.Errorf("open pricing override %q: %w", path, err)
	}
	if os.IsNotExist(err) {
		override = nil
	}
	if override != nil {
		defer override.Close()
	}
	table, err := pricinglib.Load(overrideReader(override))
	if err != nil {
		return pricinglib.Table{}, fmt.Errorf("load pricing override %q: %w", path, err)
	}
	userPath := filepath.Join(configDir, userRatesFileName)
	rates, err := readUserRates(userPath)
	if err != nil {
		return pricinglib.Table{}, err
	}
	table = table.ApplyUserRates(rates)
	return table, nil
}

func overrideReader(reader *os.File) io.Reader {
	if reader == nil {
		return nil
	}
	return reader
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
			entry.ProvenanceLabel(),
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
