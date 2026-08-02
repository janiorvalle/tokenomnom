package pages

// VaultPageData is the read-only health snapshot shown by the Vault page.
type VaultPageData struct {
	Directory          string
	Initialized        bool
	Format             string
	Files              int
	RawBytes           int64
	StoredBytes        int64
	ReclaimableBytes   int64
	RawSize            string
	StoredSize         string
	Ratio              string
	Verified           string
	VerificationState  FindingState
	LastArchive        string
	LastVerification   string
	KnownBrokenBundles int
	Reclaimable        string
	Bundles            []VaultBundle
}

// VaultBundle is one recent archive grouped from the vault manifest.
type VaultBundle struct {
	Date       string
	Files      int
	RawSize    string
	StoredSize string
	Status     string
}

// FindingState controls the visual treatment of one system finding.
type FindingState string

const (
	FindingOK      FindingState = "ok"
	FindingWarning FindingState = "warning"
	FindingMuted   FindingState = "muted"
)

// SystemFinding is one concise doctor result for the System page.
type SystemFinding struct {
	Name  string
	Value string
	State FindingState
}

// PricingRow is one effective model rate in the System page table.
type PricingRow struct {
	Model     string
	BaseInput string
	CacheRead string
	Write5m   string
	Write1h   string
	Output    string
	Status    string
	Effective string
	Source    string
	Override  string
}

// SystemSchedule contains the scheduler facts needed by the wide System page.
// It deliberately uses page-owned values instead of exposing the schedule
// package's platform-specific status type to the renderer.
type SystemSchedule struct {
	Available          bool
	Installed          bool
	DefinitionExists   bool
	BinaryExists       bool
	IntervalDrift      bool
	Mechanism          string
	ConfiguredInterval string
	InstalledInterval  string
}

// SystemSource is one provider root summary shown beside scheduler state.
type SystemSource struct {
	Name    string
	Files   int
	Size    string
	Exists  bool
	Warning bool
}

// SystemPageData contains the doctor findings and effective pricing table.
type SystemPageData struct {
	Findings          []SystemFinding
	Warnings          []string
	Pricing           []PricingRow
	PricingDisclaimer string
	Schedule          SystemSchedule
	Sources           []SystemSource
}
