// Package schedule manages tokenomnom's per-user OS scheduler definition.
package schedule

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	LaunchdLabel = "com.janiorvalle.tokenomnom"
	taskPrefix   = "tokenomnom-"
)

type Runner interface {
	Run(name string, args ...string) error
}

type Checker interface {
	Check(name string, args ...string) error
}

type Querier interface {
	Output(name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(name string, args ...string) error {
	command := exec.Command(name, args...)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (ExecRunner) Check(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func (ExecRunner) Output(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

type Options struct {
	GOOS       string
	Home       string
	ConfigDir  string
	SystemdDir string
	UID        string
	Executable string
	Interval   time.Duration
	Runner     Runner
}

type Definition struct {
	GOOS       string
	Mechanism  string
	UnitPath   string
	ExtraPaths []string
	BinaryPath string
	TaskName   string
	Interval   time.Duration
	Files      map[string][]byte
}

type Status struct {
	Installed             bool          `json:"installed"`
	DefinitionExists      bool          `json:"definition_exists"`
	Mechanism             string        `json:"mechanism"`
	UnitPath              string        `json:"unit_path"`
	BinaryPath            string        `json:"binary_path"`
	BinaryExists          bool          `json:"binary_exists"`
	InstalledInterval     time.Duration `json:"-"`
	InstalledIntervalText *string       `json:"installed_interval"`
	ConfiguredInterval    string        `json:"configured_interval"`
	IntervalDrift         bool          `json:"interval_drift"`
	TaskName              string        `json:"task_name,omitempty"`
}

func Build(options Options) (Definition, error) {
	options = defaults(options)
	definition, err := Locate(options)
	if err != nil {
		return Definition{}, err
	}
	if options.Executable == "" {
		return Definition{}, errors.New("executable path is required")
	}
	abs, err := filepath.Abs(options.Executable)
	if err != nil {
		return Definition{}, fmt.Errorf("resolve executable path: %w", err)
	}
	if options.Interval < time.Second || options.Interval%time.Second != 0 {
		return Definition{}, errors.New("schedule interval must be a positive whole-second duration")
	}
	interval := options.Interval
	if options.GOOS == "windows" && (interval < time.Minute || interval > 31*24*time.Hour) {
		return Definition{}, errors.New("Windows schedule interval must be between 1m and 744h")
	}
	definition.BinaryPath = abs
	definition.Interval = interval
	definition.Files = map[string][]byte{}
	switch options.GOOS {
	case "darwin":
		if options.UID == "" {
			return Definition{}, errors.New("user ID is required for launchd")
		}
		definition.Files[definition.UnitPath] = []byte(launchdPlist(abs, interval))
	case "linux":
		definition.Files[definition.ExtraPaths[0]] = []byte(systemdService(abs))
		definition.Files[definition.UnitPath] = []byte(systemdTimer(interval))
	case "windows":
		if options.UID == "" {
			return Definition{}, errors.New("current user ID is required for Windows Task Scheduler")
		}
		definition.Files[definition.UnitPath] = []byte(taskXML(abs, options.UID, interval))
	}
	return definition, nil
}

// Locate resolves the canonical scheduler paths without validating install-time content.
func Locate(options Options) (Definition, error) {
	options = defaults(options)
	if options.Home == "" {
		return Definition{}, errors.New("user home directory is required")
	}
	definition := Definition{GOOS: options.GOOS}
	switch options.GOOS {
	case "darwin":
		definition.Mechanism = "launchd"
		definition.UnitPath = filepath.Join(options.Home, "Library", "LaunchAgents", LaunchdLabel+".plist")
	case "linux":
		definition.Mechanism = "systemd user timer"
		base := options.SystemdDir
		if base == "" {
			base = filepath.Join(options.Home, ".config", "systemd", "user")
		}
		definition.UnitPath = filepath.Join(base, "tokenomnom.timer")
		definition.ExtraPaths = []string{filepath.Join(base, "tokenomnom.service")}
	case "windows":
		if options.UID == "" {
			return Definition{}, errors.New("current user ID is required for Windows Task Scheduler")
		}
		definition.Mechanism = "Windows Task Scheduler"
		base := options.ConfigDir
		if base == "" {
			base = filepath.Join(options.Home, "AppData", "Roaming", "tokenomnom")
		}
		definition.UnitPath = filepath.Join(base, "tokenomnom-schedule.xml")
		definition.TaskName = windowsTaskName(options.UID)
	default:
		return Definition{}, fmt.Errorf("schedule is not supported on %s", options.GOOS)
	}
	return definition, nil
}

func Install(options Options) (Status, error) {
	options = defaults(options)
	definition, err := Build(options)
	if err != nil {
		return Status{}, err
	}
	for path, contents := range definition.Files {
		if err := atomicWrite(path, contents); err != nil {
			return Status{}, err
		}
	}
	runner := options.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	switch definition.GOOS {
	case "darwin":
		domain := "gui/" + options.UID
		_ = runner.Run("launchctl", "bootout", domain, definition.UnitPath)
		if err := runner.Run("launchctl", "bootstrap", domain, definition.UnitPath); err != nil {
			if fallbackErr := runner.Run("launchctl", "load", "-w", definition.UnitPath); fallbackErr != nil {
				return Status{}, errors.Join(err, fallbackErr)
			}
		}
	case "linux":
		if err := runner.Run("systemctl", "--user", "daemon-reload"); err != nil {
			return Status{}, err
		}
		if err := runner.Run("systemctl", "--user", "enable", "--now", "tokenomnom.timer"); err != nil {
			return Status{}, err
		}
	case "windows":
		if err := runner.Run("schtasks", "/Create", "/TN", definition.TaskName, "/XML", definition.UnitPath, "/F"); err != nil {
			return Status{}, err
		}
	}
	return Inspect(options)
}

func Inspect(options Options) (Status, error) {
	options = defaults(options)
	definition, err := Locate(options)
	if err != nil {
		return Status{}, err
	}
	result := Status{Mechanism: definition.Mechanism, UnitPath: definition.UnitPath, ConfiguredInterval: options.Interval.String(), TaskName: definition.TaskName}
	result.Installed = registered(options, definition)
	result.DefinitionExists, err = anyDefinitionExists(definition)
	if err != nil {
		return result, err
	}
	var contents []byte
	if definition.GOOS == "windows" && result.Installed {
		runner := options.Runner
		if runner == nil {
			runner = ExecRunner{}
		}
		if querier, ok := runner.(Querier); ok {
			contents, err = querier.Output("schtasks", "/Query", "/TN", definition.TaskName, "/XML")
		}
	}
	if len(contents) == 0 {
		contents, err = os.ReadFile(definition.UnitPath)
	}
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(contents) == 0) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("read schedule definition: %w", err)
	}
	result.BinaryPath, result.InstalledInterval, err = parseDefinition(definition.GOOS, contents, definition)
	if err != nil {
		return result, err
	}
	if result.BinaryPath != "" {
		_, statErr := os.Stat(result.BinaryPath)
		result.BinaryExists = statErr == nil
	}
	installedInterval := result.InstalledInterval.String()
	result.InstalledIntervalText = &installedInterval
	result.IntervalDrift = result.InstalledInterval != options.Interval
	return result, nil
}

func Uninstall(options Options) error {
	options = defaults(options)
	definition, err := Locate(options)
	if err != nil {
		return err
	}
	definitionExists, err := anyDefinitionExists(definition)
	if err != nil {
		return err
	}
	isRegistered := registered(options, definition)
	_, unitStatErr := os.Stat(definition.UnitPath)
	unitExists := unitStatErr == nil
	if unitStatErr != nil && !errors.Is(unitStatErr, os.ErrNotExist) {
		return unitStatErr
	}
	if !definitionExists && !isRegistered {
		return errors.New("schedule is not installed")
	}
	runner := options.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	switch definition.GOOS {
	case "darwin":
		if isRegistered {
			domain := "gui/" + options.UID
			if err := runner.Run("launchctl", "bootout", domain+"/"+LaunchdLabel); err != nil {
				if !definitionExists {
					return err
				}
				if fallbackErr := runner.Run("launchctl", "unload", "-w", definition.UnitPath); fallbackErr != nil {
					return errors.Join(err, fallbackErr)
				}
			}
		}
	case "linux":
		if isRegistered || unitExists {
			if err := runner.Run("systemctl", "--user", "disable", "--now", "tokenomnom.timer"); err != nil {
				return err
			}
		}
	case "windows":
		if isRegistered {
			if err := runner.Run("schtasks", "/Delete", "/TN", definition.TaskName, "/F"); err != nil {
				return err
			}
		}
	}
	paths := append([]string{definition.UnitPath}, definition.ExtraPaths...)
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove schedule definition %s: %w", path, err)
		}
	}
	if definition.GOOS == "linux" {
		if err := runner.Run("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
	}
	return nil
}

func anyDefinitionExists(definition Definition) (bool, error) {
	paths := append([]string{definition.UnitPath}, definition.ExtraPaths...)
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func defaults(options Options) Options {
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.UID == "" {
		if current, err := user.Current(); err == nil {
			options.UID = current.Uid
		}
	}
	return options
}

func launchdPlist(binary string, interval time.Duration) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>sync</string><string>--scheduled</string></array>
  <key>StartInterval</key><integer>%d</integer>
  <key>RunAtLoad</key><true/>
</dict></plist>
`, xmlText(LaunchdLabel), xmlText(binary), int64(interval/time.Second))
}

func systemdService(binary string) string {
	return fmt.Sprintf(`[Unit]
Description=Refresh tokenomnom usage data

[Service]
Type=oneshot
ExecStart=%s sync --scheduled
`, quoteSystemd(binary))
}

func systemdTimer(interval time.Duration) string {
	return fmt.Sprintf(`[Unit]
Description=Refresh tokenomnom usage data periodically

[Timer]
OnActiveSec=1s
OnUnitActiveSec=%ds
Persistent=true
Unit=tokenomnom.service

[Install]
WantedBy=timers.target
`, int64(interval/time.Second))
}

func taskXML(binary, userID string, interval time.Duration) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers><TimeTrigger><StartBoundary>2000-01-01T00:00:00</StartBoundary><Enabled>true</Enabled><Repetition><Interval>PT%dS</Interval><StopAtDurationEnd>false</StopAtDurationEnd></Repetition></TimeTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>%s</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><StartWhenAvailable>true</StartWhenAvailable><Enabled>true</Enabled></Settings>
  <Actions Context="Author"><Exec><Command>%s</Command><Arguments>sync --scheduled</Arguments></Exec></Actions>
</Task>
`, int64(interval/time.Second), xmlText(userID), xmlText(binary))
}

func registered(options Options, definition Definition) bool {
	runner := options.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	checker, ok := runner.(Checker)
	if !ok {
		_, err := os.Stat(definition.UnitPath)
		return err == nil
	}
	var err error
	switch definition.GOOS {
	case "darwin":
		err = checker.Check("launchctl", "print", "gui/"+options.UID+"/"+LaunchdLabel)
	case "linux":
		err = checker.Check("systemctl", "--user", "is-enabled", "tokenomnom.timer")
	case "windows":
		err = checker.Check("schtasks", "/Query", "/TN", definition.TaskName)
	}
	return err == nil
}

func windowsTaskName(userID string) string {
	hash := sha256.Sum256([]byte(userID))
	return taskPrefix + hex.EncodeToString(hash[:6])
}

func xmlText(value string) string {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(value))
	return output.String()
}

func quoteSystemd(value string) string {
	value = strings.ReplaceAll(value, "%", "%%")
	return strconv.Quote(value)
}

func atomicWrite(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create schedule directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".tokenomnom-schedule-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(contents); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("replace schedule definition: %w", err)
		}
		if retryErr := os.Rename(tempPath, path); retryErr != nil {
			return fmt.Errorf("replace schedule definition: %w", retryErr)
		}
	}
	return nil
}

func parseDefinition(goos string, contents []byte, definition Definition) (string, time.Duration, error) {
	switch goos {
	case "darwin":
		return parseLaunchd(contents)
	case "linux":
		service, err := os.ReadFile(definition.ExtraPaths[0])
		if err != nil {
			return "", 0, fmt.Errorf("read systemd service: %w", err)
		}
		binary, err := parseSystemdBinary(string(service))
		if err != nil {
			return "", 0, err
		}
		interval, err := parseSystemdInterval(string(contents))
		return binary, interval, err
	case "windows":
		return parseTaskXML(contents)
	default:
		return "", 0, fmt.Errorf("unsupported scheduler platform %s", goos)
	}
}

func parseLaunchd(contents []byte) (string, time.Duration, error) {
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	var key, binary string
	var interval time.Duration
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", 0, fmt.Errorf("parse launchd plist: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		var value string
		switch start.Name.Local {
		case "key", "string", "integer":
			if err := decoder.DecodeElement(&value, &start); err != nil {
				return "", 0, err
			}
		}
		switch start.Name.Local {
		case "key":
			key = value
		case "string":
			if key == "ProgramArguments" && binary == "" {
				binary = value
			}
		case "integer":
			if key == "StartInterval" {
				seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
				if err != nil {
					return "", 0, err
				}
				interval = time.Duration(seconds) * time.Second
			}
		}
	}
	if binary == "" || interval <= 0 {
		return "", 0, errors.New("launchd definition is missing ProgramArguments or StartInterval")
	}
	return binary, interval, nil
}

func parseSystemdBinary(contents string) (string, error) {
	for _, line := range strings.Split(contents, "\n") {
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		value := strings.TrimPrefix(line, "ExecStart=")
		if !strings.HasPrefix(value, `"`) {
			fields := strings.Fields(value)
			if len(fields) == 0 {
				return "", errors.New("systemd service has an empty ExecStart")
			}
			return fields[0], nil
		}
		end, escaped := -1, false
		for index := 1; index < len(value); index++ {
			if value[index] == '\\' && !escaped {
				escaped = true
				continue
			}
			if value[index] == '"' && !escaped {
				end = index
				break
			}
			escaped = false
		}
		if end < 0 {
			return "", errors.New("invalid quoted systemd ExecStart")
		}
		quoted := value[:end+1]
		binary, err := strconv.Unquote(quoted)
		return strings.ReplaceAll(binary, "%%", "%"), err
	}
	return "", errors.New("systemd service is missing ExecStart")
}

func parseSystemdInterval(contents string) (time.Duration, error) {
	for _, line := range strings.Split(contents, "\n") {
		if strings.HasPrefix(line, "OnUnitActiveSec=") {
			return time.ParseDuration(strings.TrimPrefix(line, "OnUnitActiveSec="))
		}
	}
	return 0, errors.New("systemd timer is missing OnUnitActiveSec")
}

func parseTaskXML(contents []byte) (string, time.Duration, error) {
	contents = normalizeTaskXML(contents)
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	var binary, intervalText string
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", 0, fmt.Errorf("parse task XML: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "Command":
			if err := decoder.DecodeElement(&binary, &start); err != nil {
				return "", 0, err
			}
		case "Interval":
			if err := decoder.DecodeElement(&intervalText, &start); err != nil {
				return "", 0, err
			}
		}
	}
	if binary == "" || intervalText == "" {
		return "", 0, errors.New("task XML is missing Command or repetition Interval")
	}
	interval, err := parseISODuration(intervalText)
	if err != nil {
		return "", 0, err
	}
	return binary, interval, nil
}

func normalizeTaskXML(contents []byte) []byte {
	var order binary.ByteOrder
	start := 0
	if len(contents) >= 2 && contents[0] == 0xff && contents[1] == 0xfe {
		order, start = binary.LittleEndian, 2
	} else if len(contents) >= 2 && contents[0] == 0xfe && contents[1] == 0xff {
		order, start = binary.BigEndian, 2
	} else if len(contents) >= 4 && contents[1] == 0 && contents[3] == 0 {
		order = binary.LittleEndian
	}
	if order == nil {
		return contents
	}
	values := make([]uint16, 0, (len(contents)-start)/2)
	for index := start; index+1 < len(contents); index += 2 {
		values = append(values, order.Uint16(contents[index:index+2]))
	}
	decoded := []byte(string(utf16.Decode(values)))
	decoded = bytes.Replace(decoded, []byte(`encoding="UTF-16"`), []byte(`encoding="UTF-8"`), 1)
	decoded = bytes.Replace(decoded, []byte(`encoding="utf-16"`), []byte(`encoding="UTF-8"`), 1)
	return decoded
}

var isoDurationPattern = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

func parseISODuration(value string) (time.Duration, error) {
	match := isoDurationPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("unsupported task interval %q", value)
	}
	units := []time.Duration{24 * time.Hour, time.Hour, time.Minute, time.Second}
	var result time.Duration
	for index, text := range match[1:] {
		if text == "" {
			continue
		}
		amount, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return 0, err
		}
		result += time.Duration(amount) * units[index]
	}
	if result <= 0 {
		return 0, fmt.Errorf("task interval %q must be positive", value)
	}
	return result, nil
}
