package update

import "time"

type Manager string

const (
	ManagerGoMod         Manager = "gomod"
	ManagerCargo         Manager = "cargo"
	ManagerNPM           Manager = "npm"
	ManagerNuGet         Manager = "nuget"
	ManagerGitHubActions Manager = "github-actions"
	ManagerDocker        Manager = "docker"
	ManagerCustom        Manager = "custom"
)

var DefaultManagers = []Manager{
	ManagerGoMod,
	ManagerCargo,
	ManagerNPM,
	ManagerNuGet,
	ManagerGitHubActions,
	ManagerDocker,
}

type Candidate struct {
	Manager        Manager `json:"manager"`
	Datasource     string  `json:"datasource"`
	Name           string  `json:"name"`
	CurrentVersion string  `json:"currentVersion"`
	CurrentValue   string  `json:"-"`
	File           string  `json:"file"`
	Line           int     `json:"line"`
	Start          int     `json:"-"`
	End            int     `json:"-"`
	Prefix         string  `json:"-"`
	Suffix         string  `json:"-"`
}

type Update struct {
	Candidate
	LatestVersion string `json:"latestVersion,omitempty"`
	LatestDigest  string `json:"latestDigest,omitempty"`
	UpdateType    string `json:"updateType,omitempty"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
}

type Summary struct {
	Detected   int `json:"detected"`
	Current    int `json:"current"`
	Outdated   int `json:"outdated"`
	Unresolved int `json:"unresolved"`
	Ignored    int `json:"ignored"`
}

type Report struct {
	SchemaVersion int       `json:"schemaVersion"`
	GeneratedAt   time.Time `json:"generatedAt"`
	Root          string    `json:"root"`
	Summary       Summary   `json:"summary"`
	Updates       []Update  `json:"updates"`
}

type Resolution struct {
	Version string
	Digest  string
}
