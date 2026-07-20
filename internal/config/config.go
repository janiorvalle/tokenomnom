// Package config loads and validates tokenomnom's user configuration.
package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

const FileName = "config.toml"

const (
	KeyCodexDir        = "discovery.codex_dir"
	KeyClaudeDir       = "discovery.claude_dir"
	KeyTimezone        = "sync.timezone"
	KeyColor           = "reports.color"
	KeyCharts          = "reports.charts"
	KeyDailyLast       = "reports.daily_last"
	KeyDefaultProvider = "reports.default_provider"
	KeyBackupEnabled   = "backup.enabled"
	KeyBackupInterval  = "backup.interval"
	KeyBackupDir       = "backup.dir"
	KeyBackupKeep      = "backup.keep"
	KeyVaultDir        = "vault.dir"
	KeyVaultMinAge     = "vault.min_age"
	KeyVaultProviders  = "vault.providers"
	KeyVaultAuto       = "vault.auto"
)

var keys = []string{
	KeyCodexDir, KeyClaudeDir, KeyTimezone, KeyColor, KeyCharts, KeyDailyLast,
	KeyDefaultProvider, KeyBackupEnabled, KeyBackupInterval, KeyBackupDir, KeyBackupKeep,
	KeyVaultDir, KeyVaultMinAge, KeyVaultProviders, KeyVaultAuto,
}

type Config struct {
	Discovery Discovery `toml:"discovery" json:"discovery"`
	Sync      Sync      `toml:"sync" json:"sync"`
	Reports   Reports   `toml:"reports" json:"reports"`
	Backup    Backup    `toml:"backup" json:"backup"`
	Vault     Vault     `toml:"vault" json:"vault"`
}

type Discovery struct {
	CodexDir  string `toml:"codex_dir" json:"codex_dir"`
	ClaudeDir string `toml:"claude_dir" json:"claude_dir"`
}

type Sync struct {
	Timezone string `toml:"timezone" json:"timezone"`
}

type Reports struct {
	Color           string `toml:"color" json:"color"`
	Charts          bool   `toml:"charts" json:"charts"`
	DailyLast       int    `toml:"daily_last" json:"daily_last"`
	DefaultProvider string `toml:"default_provider" json:"default_provider"`
}

type Backup struct {
	Enabled  bool   `toml:"enabled" json:"enabled"`
	Interval string `toml:"interval" json:"interval"`
	Dir      string `toml:"dir" json:"dir"`
	Keep     int    `toml:"keep" json:"keep"`
}

type Vault struct {
	Dir       string   `toml:"dir" json:"dir"`
	MinAge    string   `toml:"min_age" json:"min_age"`
	Providers []string `toml:"providers" json:"providers"`
	Auto      bool     `toml:"auto" json:"auto"`
}

func Defaults() Config {
	return Config{
		Reports: Reports{Color: "auto", Charts: true, DailyLast: 30},
		Backup:  Backup{Enabled: true, Interval: "24h", Keep: 14},
		Vault:   Vault{MinAge: "168h", Providers: []string{"codex", "claude"}, Auto: true},
	}
}

// Overrides contains only explicitly changed command-line flags.
type Overrides struct {
	CodexDir       *string
	ClaudeDir      *string
	Timezone       *string
	NoColor        bool
	NoColorChanged bool
	NoChart        bool
	NoChartChanged bool
	DailyLast      *int
	Provider       *string
}

type LoadOptions struct {
	Path      string
	Home      string
	Getenv    func(string) string
	LookupEnv func(string) (string, bool)
	Output    io.Writer
	Flags     Overrides
}

type Loaded struct {
	Config  Config            `json:"config"`
	Sources map[string]string `json:"sources"`
	Path    string            `json:"path"`
	Found   bool              `json:"found"`
}

func Load(options LoadOptions) (Loaded, error) {
	getenv := options.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	path := options.Path
	if path == "" {
		var err error
		path, err = Path(xdg.Options{Home: options.Home, Getenv: getenv})
		if err != nil {
			return Loaded{}, err
		}
	}
	loaded := Loaded{Config: Defaults(), Sources: make(map[string]string, len(keys)), Path: path}
	for _, key := range keys {
		loaded.Sources[key] = "default"
	}

	data, err := os.ReadFile(path)
	if err == nil {
		loaded.Found = true
		metadata, decodeErr := toml.Decode(string(data), &loaded.Config)
		if decodeErr != nil {
			return Loaded{}, fmt.Errorf("read config %s: %w", path, decodeErr)
		}
		for _, key := range keys {
			if metadata.IsDefined(strings.Split(key, ".")...) {
				loaded.Sources[key] = "config"
			}
		}
		if undecoded := metadata.Undecoded(); len(undecoded) > 0 && options.Output != nil {
			unknown := make([]string, 0, len(undecoded))
			for _, key := range undecoded {
				unknown = append(unknown, key.String())
			}
			sort.Strings(unknown)
			fmt.Fprintf(options.Output, "warning: unknown config keys: %s\n", strings.Join(unknown, ", "))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Loaded{}, fmt.Errorf("read config %s: %w", path, err)
	}

	lookupEnv := options.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	applyEnvironment(&loaded, getenv, lookupEnv)
	applyFlags(&loaded, options.Flags)
	if err := Validate(loaded.Config); err != nil {
		var invalid validationError
		_ = errors.As(err, &invalid)
		if invalid.Key == KeyTimezone && loaded.Sources[KeyTimezone] == "flag" && loaded.Config.Sync.Timezone != "" {
			return Loaded{}, fmt.Errorf("invalid timezone %q: %w", loaded.Config.Sync.Timezone, err)
		}
		if invalid.Key == KeyDailyLast && loaded.Sources[KeyDailyLast] == "flag" {
			return Loaded{}, fmt.Errorf("--last must be greater than zero")
		}
		if invalid.Key == KeyDefaultProvider && loaded.Sources[KeyDefaultProvider] == "flag" {
			return Loaded{}, fmt.Errorf("invalid --provider %q (expected codex or claude)", loaded.Config.Reports.DefaultProvider)
		}
		return Loaded{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return loaded, nil
}

type validationError struct {
	Key     string
	Message string
}

func (err validationError) Error() string { return err.Key + " " + err.Message }

func applyEnvironment(loaded *Loaded, getenv func(string) string, lookupEnv func(string) (string, bool)) {
	applyStringEnv := func(key string, target *string, names ...string) {
		for _, name := range names {
			if value := getenv(name); value != "" {
				*target = value
				loaded.Sources[key] = "env " + name
				return
			}
		}
	}
	applyStringEnv(KeyCodexDir, &loaded.Config.Discovery.CodexDir, "TOKENOMNOM_CODEX_DIR", "CODEX_HOME")
	applyStringEnv(KeyClaudeDir, &loaded.Config.Discovery.ClaudeDir, "TOKENOMNOM_CLAUDE_DIR", "CLAUDE_CONFIG_DIR")
	if _, present := lookupEnv("NO_COLOR"); present {
		loaded.Config.Reports.Color = "never"
		loaded.Sources[KeyColor] = "env NO_COLOR"
	}
}

func applyFlags(loaded *Loaded, flags Overrides) {
	if flags.CodexDir != nil {
		loaded.Config.Discovery.CodexDir = *flags.CodexDir
		loaded.Sources[KeyCodexDir] = "flag"
	}
	if flags.ClaudeDir != nil {
		loaded.Config.Discovery.ClaudeDir = *flags.ClaudeDir
		loaded.Sources[KeyClaudeDir] = "flag"
	}
	if flags.Timezone != nil {
		loaded.Config.Sync.Timezone = *flags.Timezone
		loaded.Sources[KeyTimezone] = "flag"
	}
	if flags.NoColorChanged {
		if flags.NoColor {
			loaded.Config.Reports.Color = "never"
		} else {
			loaded.Config.Reports.Color = "auto"
		}
		loaded.Sources[KeyColor] = "flag"
	}
	if flags.NoChartChanged {
		loaded.Config.Reports.Charts = !flags.NoChart
		loaded.Sources[KeyCharts] = "flag"
	}
	if flags.DailyLast != nil {
		loaded.Config.Reports.DailyLast = *flags.DailyLast
		loaded.Sources[KeyDailyLast] = "flag"
	}
	if flags.Provider != nil {
		loaded.Config.Reports.DefaultProvider = *flags.Provider
		loaded.Sources[KeyDefaultProvider] = "flag"
	}
}

func Validate(value Config) error {
	switch value.Reports.Color {
	case "auto", "always", "never":
	default:
		return validationError{KeyColor, "must be auto, always, or never"}
	}
	if value.Reports.DailyLast <= 0 {
		return validationError{KeyDailyLast, "must be a positive integer"}
	}
	switch value.Reports.DefaultProvider {
	case "", "codex", "claude":
	default:
		return validationError{KeyDefaultProvider, "must be empty, codex, or claude"}
	}
	interval, err := time.ParseDuration(value.Backup.Interval)
	if err != nil || interval <= 0 {
		return validationError{KeyBackupInterval, "must be a positive Go duration"}
	}
	if value.Backup.Keep < 0 {
		return validationError{KeyBackupKeep, "must be 0 or a positive integer"}
	}
	minAge, err := time.ParseDuration(value.Vault.MinAge)
	if err != nil || minAge < 0 {
		return validationError{KeyVaultMinAge, "must be a non-negative Go duration"}
	}
	seenProviders := make(map[string]bool, len(value.Vault.Providers))
	for _, provider := range value.Vault.Providers {
		if provider != "codex" && provider != "claude" {
			return validationError{KeyVaultProviders, fmt.Sprintf("contains unknown provider %q", provider)}
		}
		if seenProviders[provider] {
			return validationError{KeyVaultProviders, fmt.Sprintf("contains duplicate provider %q", provider)}
		}
		seenProviders[provider] = true
	}
	if value.Sync.Timezone != "" {
		if _, err := time.LoadLocation(value.Sync.Timezone); err != nil {
			return validationError{KeyTimezone, fmt.Sprintf("names an invalid IANA timezone %q", value.Sync.Timezone)}
		}
	}
	return nil
}

func Path(options xdg.Options) (string, error) {
	dir, err := xdg.ConfigDir(options)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

type contextKey struct{}

func WithContext(parent context.Context, loaded Loaded) context.Context {
	return context.WithValue(parent, contextKey{}, loaded)
}

func FromContext(ctx context.Context) Loaded {
	if loaded, ok := ctx.Value(contextKey{}).(Loaded); ok {
		return loaded
	}
	return Loaded{Config: Defaults(), Sources: map[string]string{}}
}
