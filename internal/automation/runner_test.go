package automation

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/hooneedsupdates/internal/config"
	"github.com/openhoo/hooneedsupdates/internal/githubapi"
	"github.com/openhoo/hooneedsupdates/internal/update"
)

func TestRunnerPreviewPlansPRAndAutoMergeWithoutMutation(t *testing.T) {
	host := fixtureHost()
	vcs := &fakeVCS{baseSHA: strings.Repeat("a", 40), headSHA: strings.Repeat("b", 40)}
	runner := fixtureRunner(host, vcs, fixtureUpdate("minor"), false)

	results := runner.Run(context.Background(), []string{"openhoo/tool"})
	result := results[0]
	if result.Error != "" || result.Action != "would-create" || !result.AutoMergeEligible || result.AutoMergeAction != "would-enable" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if host.createCalls != 0 || host.enableCalls != 0 || len(vcs.pushes) != 0 {
		t.Fatalf("preview mutated state: host=%+v pushes=%v", host, vcs.pushes)
	}
}

func TestRunnerCreatesPRAndEnablesNativeAutoMerge(t *testing.T) {
	host := fixtureHost()
	vcs := &fakeVCS{baseSHA: strings.Repeat("a", 40), headSHA: strings.Repeat("b", 40)}
	runner := fixtureRunner(host, vcs, fixtureUpdate("patch"), true)

	result := runner.Run(context.Background(), []string{"openhoo/tool"})[0]
	if result.Error != "" || result.Action != "created" || result.PullRequestNumber != 17 || result.AutoMergeAction != "enabled" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if host.createCalls != 1 || host.enableCalls != 1 || host.enabledMethod != "squash" {
		t.Fatalf("unexpected host calls: %+v", host)
	}
	if len(vcs.pushes) != 1 || vcs.pushes[0].lease != "" || vcs.pushes[0].branch != "hooneedsupdates/updates" {
		t.Fatalf("unexpected pushes: %+v", vcs.pushes)
	}
	if !strings.Contains(host.createdBody, managedMarker) || !strings.Contains(host.createdBody, "native GitHub auto-merge") {
		t.Fatalf("unsafe or incomplete pull body: %q", host.createdBody)
	}
}

func TestRunnerDisablesExistingAutoMergeWhenPolicyStopsMatching(t *testing.T) {
	host := fixtureHost()
	autoMerge := &struct {
		MergeMethod string `json:"merge_method"`
	}{MergeMethod: "SQUASH"}
	host.pulls = []pullRequest{{
		Number: 9, NodeID: "PR_9", Body: managedMarker, State: "open", AutoMerge: autoMerge,
		Head: struct {
			SHA string `json:"sha"`
		}{SHA: strings.Repeat("c", 40)},
		Base: struct {
			Ref string `json:"ref"`
		}{Ref: "main"},
	}}
	host.refSHA = strings.Repeat("c", 40)
	host.refExists = true
	vcs := &fakeVCS{baseSHA: strings.Repeat("a", 40), headSHA: strings.Repeat("d", 40)}
	vcs.beforePush = func() error {
		if host.disableCalls != 1 {
			return errors.New("branch push happened before auto-merge was disabled")
		}
		return nil
	}
	runner := fixtureRunner(host, vcs, fixtureUpdate("major"), true)

	result := runner.Run(context.Background(), []string{"openhoo/tool"})[0]
	if result.Error != "" || result.AutoMergeEligible || result.AutoMergeAction != "disabled" || result.Action != "updated" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if host.disableCalls != 1 || host.enableCalls != 0 {
		t.Fatalf("auto-merge reconciliation calls: enable=%d disable=%d", host.enableCalls, host.disableCalls)
	}
	if len(vcs.pushes) != 1 || vcs.pushes[0].lease != strings.Repeat("c", 40) {
		t.Fatalf("push did not use exact lease: %+v", vcs.pushes)
	}
}

func TestRunnerFailsClosedForUnresolvedOrUnownedBranch(t *testing.T) {
	t.Run("unresolved", func(t *testing.T) {
		host := fixtureHost()
		vcs := &fakeVCS{baseSHA: strings.Repeat("a", 40), headSHA: strings.Repeat("b", 40)}
		updater := func(context.Context, string, bool, *githubapi.Client) (update.Report, []update.AppliedFile, error) {
			return update.Report{Summary: update.Summary{Outdated: 1, Unresolved: 1}}, nil, nil
		}
		result := fixtureRunner(host, vcs, updater, true).Run(context.Background(), []string{"openhoo/tool"})[0]
		if !strings.Contains(result.Error, "partial plan") || vcs.commitCalls != 0 || host.createCalls != 0 {
			t.Fatalf("did not fail closed: result=%+v vcs=%+v host=%+v", result, vcs, host)
		}
	})

	t.Run("unowned branch", func(t *testing.T) {
		host := fixtureHost()
		host.refExists = true
		host.refSHA = strings.Repeat("e", 40)
		vcs := &fakeVCS{baseSHA: strings.Repeat("a", 40), headSHA: strings.Repeat("b", 40)}
		result := fixtureRunner(host, vcs, fixtureUpdate("patch"), true).Run(context.Background(), []string{"openhoo/tool"})[0]
		if !strings.Contains(result.Error, "unowned branch") || vcs.commitCalls != 0 || len(vcs.pushes) != 0 {
			t.Fatalf("unowned branch was not protected: result=%+v vcs=%+v", result, vcs)
		}
	})

	t.Run("modified closed branch", func(t *testing.T) {
		host := fixtureHost()
		host.refExists = true
		host.refSHA = strings.Repeat("e", 40)
		closed := pullRequest{Number: 2, Body: managedMarker, State: "closed"}
		closed.Head.SHA = strings.Repeat("f", 40)
		host.pulls = []pullRequest{closed}
		vcs := &fakeVCS{baseSHA: strings.Repeat("a", 40), headSHA: strings.Repeat("b", 40)}
		result := fixtureRunner(host, vcs, fixtureUpdate("patch"), true).Run(context.Background(), []string{"openhoo/tool"})[0]
		if !strings.Contains(result.Error, "unowned branch") {
			t.Fatalf("modified closed branch was treated as owned: %+v", result)
		}
	})
}

func TestRunnerClosesStaleManagedPRAndBranch(t *testing.T) {
	host := fixtureHost()
	open := pullRequest{Number: 5, NodeID: "PR_5", Body: managedMarker, State: "open", HTMLURL: "https://github.test/pull/5"}
	open.Head.SHA = strings.Repeat("c", 40)
	open.Base.Ref = "main"
	host.pulls = []pullRequest{open}
	host.refExists = true
	host.refSHA = open.Head.SHA
	vcs := &fakeVCS{baseSHA: strings.Repeat("a", 40)}
	updater := func(context.Context, string, bool, *githubapi.Client) (update.Report, []update.AppliedFile, error) {
		return update.Report{PlanDigest: "sha256:current"}, nil, nil
	}

	result := fixtureRunner(host, vcs, updater, true).Run(context.Background(), []string{"openhoo/tool"})[0]
	if result.Error != "" || result.Action != "closed" || host.closeCalls != 1 || host.deleteCalls != 1 {
		t.Fatalf("stale lifecycle failed: result=%+v host=%+v", result, host)
	}
}

func TestRunnerDefersRemainingFleetOnGitHubRateLimit(t *testing.T) {
	retryAt := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	host := fixtureHost()
	host.repositoryErr = &githubapi.RateLimitError{
		Kind: "secondary", RetryAt: retryAt, Attempts: 2, Persisted: true,
	}
	runner := fixtureRunner(
		host,
		&fakeVCS{baseSHA: strings.Repeat("a", 40), headSHA: strings.Repeat("b", 40)},
		fixtureUpdate("patch"),
		true,
	)
	results := runner.Run(context.Background(), []string{"openhoo/one", "openhoo/two", "openhoo/three"})
	if len(results) != 3 || host.repositoryCalls != 1 {
		t.Fatalf("results=%+v repository calls=%d", results, host.repositoryCalls)
	}
	for _, result := range results {
		if result.Action != "deferred" || result.Error != "" || result.RetryAt != retryAt.Format(time.RFC3339) ||
			result.DeferralReason != "GitHub secondary rate limit" {
			t.Fatalf("unexpected deferred result: %+v", result)
		}
	}
}

func TestAutoMergePolicyRequiresEveryUpdateToMatch(t *testing.T) {
	settings := config.Default().Automation
	settings.AutoMerge.Enabled = true
	settings.AutoMerge.UpdateTypes = []string{"patch", "minor"}
	settings.AutoMerge.Managers = []string{"github-actions", "custom"}
	settings.AutoMerge.Dependencies = []string{`^openhoo/`}
	repository := fixtureRepository()

	cases := []struct {
		name   string
		update update.Update
		count  int
		want   string
	}{
		{name: "allowed", update: reportUpdate("patch", update.ManagerGitHubActions, "openhoo/tool"), count: 1},
		{name: "major", update: reportUpdate("major", update.ManagerGitHubActions, "openhoo/tool"), count: 1, want: "major update"},
		{name: "manager", update: reportUpdate("patch", update.ManagerGoMod, "openhoo/tool"), count: 1, want: "manager gomod"},
		{name: "dependency", update: reportUpdate("patch", update.ManagerGitHubActions, "actions/checkout"), count: 1, want: "dependency actions/checkout"},
		{name: "limit", update: reportUpdate("patch", update.ManagerGitHubActions, "openhoo/tool"), count: 21, want: "exceed maxUpdates"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			report := update.Report{Summary: update.Summary{Outdated: test.count}, Updates: []update.Update{test.update}}
			eligible, reason := autoMergeDecision(settings, repository, report)
			if test.want == "" && !eligible {
				t.Fatalf("allowed update rejected: %s", reason)
			}
			if test.want != "" && (eligible || !strings.Contains(reason, test.want)) {
				t.Fatalf("unsafe update accepted or wrong reason: eligible=%v reason=%q", eligible, reason)
			}
		})
	}
}

func TestSelectReportKeepsMatchingUnresolvedAndExcludesUnrelatedUpdates(t *testing.T) {
	report := update.Report{
		SchemaVersion: 2,
		Updates: []update.Update{
			reportUpdate("minor", update.ManagerGitHubActions, "openhoo/hooversion"),
			reportUpdate("patch", update.ManagerGoMod, "example.com/unrelated"),
			{Candidate: update.Candidate{Manager: update.ManagerCustom, Name: "openhoo/hoolicy"}, Status: "unresolved", Error: "rate limited"},
		},
	}
	selection := config.Selection{
		UpdateTypes:  []string{"patch", "minor"},
		Managers:     []string{"github-actions", "custom"},
		Dependencies: []string{`^openhoo/`},
	}
	filtered := SelectReport(report, selection)
	if len(filtered.Updates) != 2 || filtered.Summary.Outdated != 1 || filtered.Summary.Unresolved != 1 {
		t.Fatalf("unexpected filtered report: %+v", filtered)
	}
	if filtered.PlanDigest == "" || filtered.PlanDigest == report.PlanDigest {
		t.Fatalf("filtered plan digest was not recomputed: %q", filtered.PlanDigest)
	}
}

func fixtureRunner(host *fakeHost, vcs *fakeVCS, updater Updater, write bool) *Runner {
	settings := config.Default().Automation
	settings.AutoMerge.Enabled = true
	settings.AutoMerge.Dependencies = []string{`^openhoo/`}
	return &Runner{settings: settings, write: write, host: host, vcs: vcs, updater: updater}
}

func fixtureUpdate(kind string) Updater {
	return func(context.Context, string, bool, *githubapi.Client) (update.Report, []update.AppliedFile, error) {
		entry := reportUpdate(kind, update.ManagerGitHubActions, "openhoo/dependency")
		return update.Report{
			PlanDigest: "sha256:fixture",
			Summary:    update.Summary{Detected: 1, Outdated: 1},
			Updates:    []update.Update{entry},
		}, []update.AppliedFile{{Path: ".github/workflows/ci.yml", Kind: "manifest", Updates: 1}}, nil
	}
}

func reportUpdate(kind string, manager update.Manager, name string) update.Update {
	return update.Update{
		Candidate:     update.Candidate{Manager: manager, Name: name, CurrentVersion: "1.0.0", File: "fixture"},
		LatestVersion: "1.1.0", UpdateType: kind, Status: "outdated",
	}
}

func fixtureRepository() repository {
	repository := repository{
		FullName: "openhoo/tool", DefaultBranch: "main", CloneURL: "https://github.test/openhoo/tool.git",
		AllowAutoMerge: true,
	}
	repository.Owner.Login = "openhoo"
	return repository
}

type fakeHost struct {
	repository      repository
	repositoryErr   error
	repositoryCalls int
	pulls           []pullRequest
	refSHA          string
	refExists       bool

	createCalls  int
	updateCalls  int
	closeCalls   int
	deleteCalls  int
	enableCalls  int
	disableCalls int
	labelCalls   int

	createdBody   string
	enabledMethod string
}

func fixtureHost() *fakeHost { return &fakeHost{repository: fixtureRepository()} }

func (h *fakeHost) Repository(context.Context, string) (repository, error) {
	h.repositoryCalls++
	return h.repository, h.repositoryErr
}
func (h *fakeHost) Ref(context.Context, string, string) (string, bool, error) {
	return h.refSHA, h.refExists, nil
}
func (h *fakeHost) Pulls(context.Context, string, string, string, string) ([]pullRequest, error) {
	return append([]pullRequest(nil), h.pulls...), nil
}
func (h *fakeHost) CreatePull(_ context.Context, _ string, title, body, _, base string, draft bool) (pullRequest, error) {
	h.createCalls++
	h.createdBody = body
	pull := pullRequest{Number: 17, NodeID: "PR_17", HTMLURL: "https://github.test/pull/17", Title: title, Body: body, State: "open", Draft: draft}
	pull.Base.Ref = base
	return pull, nil
}
func (h *fakeHost) UpdatePull(_ context.Context, _ string, number int, title, body, base string) (pullRequest, error) {
	h.updateCalls++
	pull := pullRequest{Number: number, NodeID: "PR_9", HTMLURL: "https://github.test/pull/9", Title: title, Body: body, State: "open"}
	pull.Base.Ref = base
	if len(h.pulls) > 0 {
		pull.AutoMerge = h.pulls[0].AutoMerge
	}
	return pull, nil
}
func (h *fakeHost) ClosePull(context.Context, string, int) error { h.closeCalls++; return nil }
func (h *fakeHost) DeleteRef(context.Context, string, string) error {
	h.deleteCalls++
	return nil
}
func (h *fakeHost) AddLabels(context.Context, string, int, []string) error {
	h.labelCalls++
	return nil
}
func (h *fakeHost) EnableAutoMerge(_ context.Context, _ string, method string) error {
	h.enableCalls++
	h.enabledMethod = method
	return nil
}
func (h *fakeHost) DisableAutoMerge(context.Context, string) error { h.disableCalls++; return nil }

type fakeVCS struct {
	baseSHA string
	headSHA string

	commitCalls int
	pushes      []fakePush
	beforePush  func() error
}

type fakePush struct{ branch, lease string }

func (v *fakeVCS) Clone(_ context.Context, _ repository, destination string) error {
	return os.MkdirAll(destination, 0o700)
}
func (v *fakeVCS) Head(context.Context, string) (string, error) { return v.baseSHA, nil }
func (v *fakeVCS) Commit(_ context.Context, _, _, _, _ string, files []update.AppliedFile) (string, []string, error) {
	v.commitCalls++
	if v.headSHA == "" {
		return "", nil, errors.New("missing fake head")
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	sort.Strings(paths)
	return v.headSHA, paths, nil
}
func (v *fakeVCS) Push(_ context.Context, _ string, branch, lease string) error {
	if v.beforePush != nil {
		if err := v.beforePush(); err != nil {
			return err
		}
	}
	v.pushes = append(v.pushes, fakePush{branch: branch, lease: lease})
	return nil
}
