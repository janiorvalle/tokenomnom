package pages

// VaultPageData is the read-only health snapshot shown by the Vault page.
type VaultPageData struct {
	Directory          string
	Initialized        bool
	Format             string
	Files              int
	RawSize            string
	StoredSize         string
	Ratio              string
	Verified           string
	VerificationState  FindingState
	LastArchive        string
	LastVerification   string
	KnownBrokenBundles int
	Reclaimable        string
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

// SystemPageData contains the doctor findings and effective pricing table.
type SystemPageData struct {
	Findings          []SystemFinding
	Warnings          []string
	Pricing           []PricingRow
	PricingDisclaimer string
}
