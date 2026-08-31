package automation

import (
	"context"
	"net/http"

	"github.com/openhoo/hooneedsupdates/internal/config"
	"github.com/openhoo/hooneedsupdates/internal/githubapi"
	"github.com/openhoo/hooneedsupdates/internal/update"
)

const managedMarker = "<!-- hooneedsupdates:managed -->"

type Options struct {
	Settings   config.Automation
	Token      string
	APIURL     string
	GraphQLURL string
	Write      bool
	HTTPClient *http.Client
	StateFile  string
	Updater    Updater
}

type Updater func(context.Context, string, bool, *githubapi.Client) (update.Report, []update.AppliedFile, error)

type Result struct {
	Repository        string `json:"repository"`
	BaseBranch        string `json:"baseBranch,omitempty"`
	BaseSHA           string `json:"baseSha,omitempty"`
	Branch            string `json:"branch,omitempty"`
	HeadSHA           string `json:"headSha,omitempty"`
	PlanDigest        string `json:"planDigest,omitempty"`
	Outdated          int    `json:"outdated"`
	Files             int    `json:"files"`
	Action            string `json:"action"`
	PullRequestNumber int    `json:"pullRequestNumber,omitempty"`
	PullRequestURL    string `json:"pullRequestUrl,omitempty"`
	AutoMergeEligible bool   `json:"autoMergeEligible"`
	AutoMergeReason   string `json:"autoMergeReason,omitempty"`
	AutoMergeAction   string `json:"autoMergeAction,omitempty"`
	RetryAt           string `json:"retryAt,omitempty"`
	DeferralReason    string `json:"deferralReason,omitempty"`
	Error             string `json:"error,omitempty"`
}

type repository struct {
	FullName       string `json:"full_name"`
	DefaultBranch  string `json:"default_branch"`
	CloneURL       string `json:"clone_url"`
	Archived       bool   `json:"archived"`
	Disabled       bool   `json:"disabled"`
	AllowAutoMerge bool   `json:"allow_auto_merge"`
	Owner          struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type pullRequest struct {
	Number  int    `json:"number"`
	NodeID  string `json:"node_id"`
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	Head    struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	AutoMerge *struct {
		MergeMethod string `json:"merge_method"`
	} `json:"auto_merge"`
}

type host interface {
	Repository(context.Context, string) (repository, error)
	Ref(context.Context, string, string) (string, bool, error)
	Pulls(context.Context, string, string, string, string) ([]pullRequest, error)
	CreatePull(context.Context, string, string, string, string, string, bool) (pullRequest, error)
	UpdatePull(context.Context, string, int, string, string, string) (pullRequest, error)
	ClosePull(context.Context, string, int) error
	DeleteRef(context.Context, string, string) error
	AddLabels(context.Context, string, int, []string) error
	EnableAutoMerge(context.Context, string, string) error
	DisableAutoMerge(context.Context, string) error
}

type vcs interface {
	Clone(context.Context, repository, string) error
	Head(context.Context, string) (string, error)
	Commit(context.Context, string, string, string, string, []update.AppliedFile) (string, []string, error)
	Push(context.Context, string, string, string) error
}

type Runner struct {
	settings config.Automation
	write    bool
	host     host
	vcs      vcs
	updater  Updater
	github   *githubapi.Client
}
