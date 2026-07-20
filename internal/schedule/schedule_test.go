package schedule

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type recordingRunner struct {
	calls      []string
	fail       map[string]error
	registered bool
}

type liveTaskRunner struct {
	recordingRunner
	output []byte
}

func (runner *liveTaskRunner) Output(string, ...string) ([]byte, error) {
	return runner.output, nil
}

func (runner *recordingRunner) Run(name string, args ...string) error {
	call := strings.Join(append([]string{name}, args...), " ")
	runner.calls = append(runner.calls, call)
	if err := runner.fail[call]; err != nil {
		return err
	}
	if strings.Contains(call, " bootstrap ") || strings.Contains(call, " load -w ") ||
		strings.Contains(call, " enable --now ") || strings.Contains(call, "schtasks /Create") {
		runner.registered = true
	}
	if strings.Contains(call, " bootout ") || strings.Contains(call, " unload -w ") ||
		strings.Contains(call, " disable --now ") || strings.Contains(call, "schtasks /Delete") {
		runner.registered = false
	}
	return nil
}

func (runner *recordingRunner) Check(string, ...string) error {
	if !runner.registered {
		return os.ErrNotExist
	}
	return nil
}

func TestBuildPlatformDefinitions(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	binary := filepath.Join(home, "bin", "tokenomnom")
	cases := []struct {
		goos      string
		mechanism string
		pathPart  string
		fragments []string
	}{
		{"darwin", "launchd", "com.janiorvalle.tokenomnom.plist", []string{"<key>Label</key><string>com.janiorvalle.tokenomnom</string>", "<string>sync</string><string>--scheduled</string>", "<integer>86400</integer>"}},
		{"linux", "systemd user timer", "tokenomnom.timer", []string{"OnActiveSec=1s", "OnUnitActiveSec=86400s", "Persistent=true", "tokenomnom.service"}},
		{"windows", "Windows Task Scheduler", "tokenomnom-schedule.xml", []string{"<Interval>PT86400S</Interval>", "<UserId>501</UserId>", "<Command>" + binary + "</Command>", "<Arguments>sync --scheduled</Arguments>", "<RunLevel>LeastPrivilege</RunLevel>"}},
	}
	for _, test := range cases {
		test := test
		t.Run(test.goos, func(t *testing.T) {
			definition, err := Build(Options{GOOS: test.goos, Home: home, ConfigDir: filepath.Join(home, "config"), UID: "501", Executable: binary, Interval: 24 * time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			if definition.Mechanism != test.mechanism || !strings.HasSuffix(definition.UnitPath, test.pathPart) {
				t.Fatalf("definition = %#v", definition)
			}
			if test.goos == "windows" && !strings.HasPrefix(definition.TaskName, taskPrefix) {
				t.Errorf("Windows task name = %q", definition.TaskName)
			}
			contents := string(definition.Files[definition.UnitPath])
			for _, fragment := range test.fragments {
				if !strings.Contains(contents, fragment) {
					t.Errorf("%s definition missing %q:\n%s", test.goos, fragment, contents)
				}
			}
			if test.goos == "linux" {
				service := string(definition.Files[definition.ExtraPaths[0]])
				if !strings.Contains(service, "ExecStart="+strconv.Quote(binary)+" sync --scheduled") {
					t.Fatalf("systemd service points at wrong command:\n%s", service)
				}
			}
		})
	}
}

func TestLinuxUsesExplicitSystemdDirectory(t *testing.T) {
	home := t.TempDir()
	systemdDir := filepath.Join(home, "xdg", "systemd", "user")
	definition, err := Build(Options{GOOS: "linux", Home: home, SystemdDir: systemdDir, Executable: filepath.Join(home, "tokenomnom"), Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if definition.UnitPath != filepath.Join(systemdDir, "tokenomnom.timer") {
		t.Fatalf("unit path = %q", definition.UnitPath)
	}
}

func TestWindowsRejectsUnsupportedIntervals(t *testing.T) {
	home := t.TempDir()
	for _, interval := range []time.Duration{time.Second, 31*24*time.Hour + time.Second} {
		if _, err := Build(Options{GOOS: "windows", Home: home, Executable: filepath.Join(home, "tokenomnom.exe"), Interval: interval}); err == nil || !strings.Contains(err.Error(), "between 1m and 744h") {
			t.Fatalf("interval %s error = %v", interval, err)
		}
	}
	if _, err := Build(Options{GOOS: "linux", Home: home, Executable: filepath.Join(home, "tokenomnom"), Interval: 1500 * time.Millisecond}); err == nil || !strings.Contains(err.Error(), "whole-second") {
		t.Fatalf("fractional interval error = %v", err)
	}
}

func TestWindowsStatusAndUninstallSurviveUninstallableCurrentInterval(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "tokenomnom.exe")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{fail: map[string]error{}}
	options := Options{GOOS: "windows", Home: home, ConfigDir: filepath.Join(home, "config"), UID: "S-1-5-21-test", Executable: binary, Interval: time.Hour, Runner: runner}
	if _, err := Install(options); err != nil {
		t.Fatal(err)
	}
	options.Interval = 30 * time.Second
	status, err := Inspect(options)
	if err != nil || !status.Installed || !status.IntervalDrift {
		t.Fatalf("status with current 30s interval = %#v, %v", status, err)
	}
	if err := Uninstall(options); err != nil {
		t.Fatalf("uninstall with current 30s interval: %v", err)
	}
}

func TestWindowsInspectUsesLiveRegisteredTask(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, "config")
	oldBinary := filepath.Join(home, "old.exe")
	newBinary := filepath.Join(home, "new.exe")
	for _, path := range []string{oldBinary, newBinary} {
		if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	options := Options{GOOS: "windows", Home: home, ConfigDir: configDir, UID: "S-1-5-21-test", Executable: oldBinary, Interval: time.Hour}
	definition, err := Build(options)
	if err != nil {
		t.Fatal(err)
	}
	for path, contents := range definition.Files {
		if err := atomicWrite(path, contents); err != nil {
			t.Fatal(err)
		}
	}
	runner := &liveTaskRunner{recordingRunner: recordingRunner{registered: true, fail: map[string]error{}}, output: []byte(taskXML(newBinary, options.UID, 2*time.Hour))}
	options.Runner = runner
	status, err := Inspect(options)
	if err != nil {
		t.Fatal(err)
	}
	if status.BinaryPath != newBinary || status.InstalledInterval != 2*time.Hour || !status.IntervalDrift {
		t.Fatalf("live task status = %#v", status)
	}
}

func TestLinuxUninstallRemovesServiceOnlyPartialInstall(t *testing.T) {
	home := t.TempDir()
	options := Options{GOOS: "linux", Home: home, Executable: filepath.Join(home, "tokenomnom"), Interval: time.Hour, Runner: &recordingRunner{fail: map[string]error{}}}
	definition, err := Build(options)
	if err != nil {
		t.Fatal(err)
	}
	service := definition.ExtraPaths[0]
	if err := atomicWrite(service, definition.Files[service]); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(service); !os.IsNotExist(err) {
		t.Fatalf("partial service remains: %v", err)
	}
}

func TestLinuxUninstallStopsDisabledTimer(t *testing.T) {
	home := t.TempDir()
	runner := &recordingRunner{fail: map[string]error{}, registered: false}
	options := Options{GOOS: "linux", Home: home, Executable: filepath.Join(home, "tokenomnom"), Interval: time.Hour, Runner: runner}
	definition, err := Build(options)
	if err != nil {
		t.Fatal(err)
	}
	for path, contents := range definition.Files {
		if err := atomicWrite(path, contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := Uninstall(options); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(runner.calls, "\n"), "disable --now tokenomnom.timer") {
		t.Fatalf("disabled timer was not stopped: %#v", runner.calls)
	}
}

func TestWindowsTaskNamesArePerUser(t *testing.T) {
	first := windowsTaskName("S-1-5-21-first")
	second := windowsTaskName("S-1-5-21-second")
	if first == second || !strings.HasPrefix(first, taskPrefix) || !strings.HasPrefix(second, taskPrefix) {
		t.Fatalf("task names = %q, %q", first, second)
	}
}

func TestLaunchdUninstallUsesLabelWhenPlistIsMissing(t *testing.T) {
	home := t.TempDir()
	runner := &recordingRunner{registered: true, fail: map[string]error{}}
	options := Options{GOOS: "darwin", Home: home, UID: "501", Executable: filepath.Join(home, "tokenomnom"), Interval: time.Hour, Runner: runner}
	if err := Uninstall(options); err != nil {
		t.Fatal(err)
	}
	want := "launchctl bootout gui/501/" + LaunchdLabel
	if !strings.Contains(strings.Join(runner.calls, "\n"), want) {
		t.Fatalf("launchd cleanup calls = %#v", runner.calls)
	}
}

func TestInstallInspectAndUninstallRoundTrip(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "bin", "tokenomnom")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{fail: map[string]error{}}
	options := Options{GOOS: "linux", Home: home, Executable: binary, Interval: 24 * time.Hour, Runner: runner}
	status, err := Install(options)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || !status.BinaryExists || status.IntervalDrift || status.BinaryPath != binary {
		t.Fatalf("installed status = %#v", status)
	}
	if len(runner.calls) != 2 || !strings.Contains(strings.Join(runner.calls, "\n"), "enable --now tokenomnom.timer") {
		t.Fatalf("install calls = %#v", runner.calls)
	}

	drifted := options
	drifted.Interval = 12 * time.Hour
	status, err = Inspect(drifted)
	if err != nil || !status.IntervalDrift {
		t.Fatalf("drift status = %#v, %v", status, err)
	}
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	status, err = Inspect(options)
	if err != nil || status.BinaryExists {
		t.Fatalf("stale binary status = %#v, %v", status, err)
	}
	if err := Uninstall(options); err != nil {
		t.Fatal(err)
	}
	status, err = Inspect(options)
	if err != nil || status.Installed {
		t.Fatalf("uninstalled status = %#v, %v", status, err)
	}
	if err := Uninstall(options); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("second uninstall error = %v", err)
	}
}

func TestLaunchdFallsBackToLoad(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "tokenomnom")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{fail: map[string]error{
		"launchctl bootstrap gui/501 " + filepath.Join(home, "Library", "LaunchAgents", LaunchdLabel+".plist"): os.ErrInvalid,
	}}
	_, err := Install(Options{GOOS: "darwin", Home: home, UID: "501", Executable: binary, Interval: time.Hour, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(runner.calls, "\n"), "launchctl load -w") {
		t.Fatalf("launchd calls = %#v", runner.calls)
	}
}
