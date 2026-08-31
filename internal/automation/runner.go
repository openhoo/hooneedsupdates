package automation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/openhoo/hooneedsupdates/internal/config"
	"github.com/openhoo/hooneedsupdates/internal/update"
)

const pullTitle = "chore(deps): update dependencies"

type repositoryState struct {
	name         string
	repository   repository
	checkout     string
	report       update.Report
	files        []update.AppliedFile
	result       Result
	openPull     *pullRequest
	branchOwned  bool
	remoteSHA    string
	branchExists bool
}

func New(options Options) (*Runner, error) {
	if options.Updater == nil {
		return nil, errors.New("automation updater is required")
	}
	if options.Write && options.Token == "" {
		return nil, errors.New("GH_TOKEN or GITHUB_TOKEN is required with --write")
	}
	host, err := newGitHubHost(options.HTTPClient, options.APIURL, options.GraphQLURL, options.Token)
	if err != nil {
		return nil, err
	}
	return &Runner{
		settings: options.Settings,
		write:    options.Write,
		host:     host,
		vcs:      gitVCS{token: options.Token},
		updater:  options.Updater,
	}, nil
}

func (r *Runner) Run(ctx context.Context, repositories []string) []Result {
	repositories = uniqueRepositories(repositories)
	results := make([]Result, 0, len(repositories))
	for _, name := range repositories {
		if err := ctx.Err(); err != nil {
			results = append(results, Result{Repository: name, Action: "failed", Error: err.Error()})
			continue
		}
		results = append(results, r.runRepository(ctx, name))
	}
	return results
}

func (r *Runner) runRepository(ctx context.Context, name string) (result Result) {
	result = Result{Repository: name, Action: "failed"}
	defer func() {
		if recovered := recover(); recovered != nil {
			result.Error = fmt.Sprintf("internal automation panic: %v", recovered)
		}
	}()

	state, cleanup, err := r.prepareRepository(ctx, name)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result = state.result
	if state.report.Summary.Unresolved > 0 {
		result.Error = unresolvedError(state.report)
		return result
	}
	if err := r.loadManagedState(ctx, state); err != nil {
		result.Error = err.Error()
		return result
	}
	if state.report.Summary.Outdated == 0 {
		return r.handleCurrent(ctx, result, name, state.openPull, state.branchOwned, state.branchExists)
	}
	return r.handleUpdates(ctx, state)
}

func (r *Runner) prepareRepository(
	ctx context.Context,
	name string,
) (*repositoryState, func(), error) {
	repository, err := r.host.Repository(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	if !strings.EqualFold(repository.FullName, name) {
		return nil, nil, fmt.Errorf("GitHub resolved %s as unexpected repository %s", name, repository.FullName)
	}
	if repository.Archived || repository.Disabled {
		return nil, nil, errors.New("repository is archived or disabled")
	}
	temporaryRoot, err := os.MkdirTemp("", "hooneedsupdates-repository-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(temporaryRoot) }
	checkout := temporaryRoot + "/repository"
	if err := r.vcs.Clone(ctx, repository, checkout); err != nil {
		return nil, cleanup, err
	}
	baseSHA, err := r.vcs.Head(ctx, checkout)
	if err != nil {
		return nil, cleanup, err
	}
	report, files, err := r.updater(ctx, checkout, r.settings.Lockfiles)
	if err != nil {
		return nil, cleanup, err
	}
	result := Result{
		Repository: name, BaseBranch: repository.DefaultBranch, BaseSHA: baseSHA,
		Branch: r.settings.BranchPrefix + "/updates", PlanDigest: report.PlanDigest,
		Outdated: report.Summary.Outdated, Files: len(files), Action: "failed",
	}
	return &repositoryState{
		name: name, repository: repository, checkout: checkout,
		report: report, files: files, result: result,
	}, cleanup, nil
}

func (r *Runner) loadManagedState(ctx context.Context, state *repositoryState) error {
	pulls, err := r.host.Pulls(
		ctx, state.name, state.repository.Owner.Login,
		state.result.Branch, state.repository.DefaultBranch,
	)
	if err != nil {
		return err
	}
	openPull, ownedHeadSHA, err := selectPull(pulls)
	if err != nil {
		return err
	}
	state.openPull = openPull
	state.remoteSHA, state.branchExists, err = r.host.Ref(ctx, state.name, state.result.Branch)
	if err != nil {
		return err
	}
	state.branchOwned = state.openPull != nil ||
		(ownedHeadSHA != "" && (!state.branchExists || state.remoteSHA == ownedHeadSHA))
	return nil
}

func (r *Runner) handleUpdates(ctx context.Context, state *repositoryState) Result {
	result := state.result
	if len(state.files) == 0 {
		result.Error = "update plan is outdated but produced no files"
		return result
	}
	if state.branchExists && state.openPull == nil && !state.branchOwned {
		result.Error = fmt.Sprintf("refusing to overwrite unowned branch %s", result.Branch)
		return result
	}
	message := pullTitle + "\n\nHooNeedsUpdates-Plan: " + state.report.PlanDigest
	headSHA, changedPaths, err := r.vcs.Commit(
		ctx, state.checkout, message, r.settings.CommitAuthor, r.settings.CommitEmail, state.files,
	)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.HeadSHA = headSHA
	result.Files = len(changedPaths)
	result.AutoMergeEligible, result.AutoMergeReason = autoMergeDecision(
		r.settings, state.repository, state.report,
	)
	body := pullBody(state.name, result, state.report, r.settings.Lockfiles)
	if !r.write {
		return r.previewUpdate(state, result, body)
	}
	return r.writeUpdate(ctx, state, result, body)
}

func (r *Runner) previewUpdate(state *repositoryState, result Result, body string) Result {
	switch {
	case state.openPull == nil:
		result.Action = "would-create"
	case state.remoteSHA == result.HeadSHA &&
		pullMatches(*state.openPull, pullTitle, body, state.repository.DefaultBranch):
		result.Action = "unchanged"
	default:
		result.Action = "would-update"
	}
	if state.openPull != nil {
		setPullResult(&result, *state.openPull)
		result.AutoMergeAction = plannedAutoMergeAction(
			*state.openPull, result.AutoMergeEligible, r.settings.MergeMethod,
		)
	} else if result.AutoMergeEligible {
		result.AutoMergeAction = "would-enable"
	}
	return result
}

func (r *Runner) writeUpdate(
	ctx context.Context,
	state *repositoryState,
	result Result,
	body string,
) Result {
	preDisabled, err := r.disableUnsafeAutoMerge(ctx, state.openPull, result.AutoMergeEligible)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	branchChanged := !state.branchExists || state.remoteSHA != result.HeadSHA
	if branchChanged {
		lease := ""
		if state.branchExists {
			lease = state.remoteSHA
		}
		if err := r.vcs.Push(ctx, state.checkout, result.Branch, lease); err != nil {
			result.Error = err.Error()
			return result
		}
	}
	pull, action, err := r.upsertPull(ctx, state, body, branchChanged)
	result.Action = action
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if preDisabled {
		pull.AutoMerge = nil
	}
	setPullResult(&result, pull)
	if err := r.host.AddLabels(ctx, state.name, pull.Number, r.settings.Labels); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := r.reconcileAutoMerge(ctx, &result, pull, preDisabled); err != nil {
		result.Error = err.Error()
	}
	return result
}

func (r *Runner) disableUnsafeAutoMerge(
	ctx context.Context,
	pull *pullRequest,
	eligible bool,
) (bool, error) {
	if pull == nil || pull.AutoMerge == nil {
		return false, nil
	}
	methodMatches := strings.EqualFold(pull.AutoMerge.MergeMethod, r.settings.MergeMethod)
	if eligible && methodMatches {
		return false, nil
	}
	if err := r.host.DisableAutoMerge(ctx, pull.NodeID); err != nil {
		return false, err
	}
	pull.AutoMerge = nil
	return true, nil
}

func (r *Runner) upsertPull(
	ctx context.Context,
	state *repositoryState,
	body string,
	branchChanged bool,
) (pullRequest, string, error) {
	if state.openPull == nil {
		pull, err := r.host.CreatePull(
			ctx, state.name, pullTitle, body, state.result.Branch,
			state.repository.DefaultBranch, r.settings.Draft,
		)
		return pull, "created", err
	}
	pull := *state.openPull
	metadataChanged := !pullMatches(pull, pullTitle, body, state.repository.DefaultBranch)
	var err error
	if metadataChanged {
		pull, err = r.host.UpdatePull(
			ctx, state.name, pull.Number, pullTitle, body, state.repository.DefaultBranch,
		)
	}
	if branchChanged || metadataChanged {
		return pull, "updated", err
	}
	return pull, "unchanged", err
}

func unresolvedError(report update.Report) string {
	details := make([]string, 0, 4)
	for _, entry := range report.Updates {
		if entry.Status != "unresolved" {
			continue
		}
		message := strings.ReplaceAll(strings.ReplaceAll(entry.Error, "\r", " "), "\n", " ")
		if len(message) > 160 {
			message = message[:157] + "..."
		}
		details = append(details, fmt.Sprintf("%s/%s: %s", entry.Manager, entry.Name, message))
		if len(details) == 4 {
			break
		}
	}
	message := fmt.Sprintf("refusing a partial plan with %d unresolved dependencies", report.Summary.Unresolved)
	if len(details) > 0 {
		message += ": " + strings.Join(details, "; ")
	}
	return message
}

func (r *Runner) handleCurrent(
	ctx context.Context,
	result Result,
	name string,
	pull *pullRequest,
	branchOwned, branchExists bool,
) Result {
	result.Action = "current"
	result.AutoMergeReason = "no outdated dependencies"
	if !r.settings.CloseStale || (pull == nil && !(branchOwned && branchExists)) {
		return result
	}
	if !r.write {
		result.Action = "would-close"
		if pull != nil {
			setPullResult(&result, *pull)
		}
		return result
	}
	if pull != nil {
		if err := r.host.ClosePull(ctx, name, pull.Number); err != nil {
			result.Action = "failed"
			result.Error = err.Error()
			return result
		}
		setPullResult(&result, *pull)
	}
	if branchExists {
		if err := r.host.DeleteRef(ctx, name, result.Branch); err != nil {
			result.Action = "failed"
			result.Error = err.Error()
			return result
		}
	}
	result.Action = "closed"
	return result
}

func (r *Runner) reconcileAutoMerge(
	ctx context.Context,
	result *Result,
	pull pullRequest,
	preDisabled bool,
) error {
	if result.AutoMergeEligible {
		if pull.AutoMerge != nil {
			result.AutoMergeAction = "already-enabled"
			return nil
		}
		if pull.NodeID == "" {
			return errors.New("GitHub pull request response has no node ID for auto-merge")
		}
		if err := r.host.EnableAutoMerge(ctx, pull.NodeID, r.settings.MergeMethod); err != nil {
			return err
		}
		result.AutoMergeAction = "enabled"
		if preDisabled {
			result.AutoMergeAction = "reconfigured"
		}
		return nil
	}
	if pull.AutoMerge == nil {
		result.AutoMergeAction = "not-eligible"
		if preDisabled {
			result.AutoMergeAction = "disabled"
		}
		return nil
	}
	if pull.NodeID == "" {
		return errors.New("GitHub pull request response has no node ID for disabling auto-merge")
	}
	if err := r.host.DisableAutoMerge(ctx, pull.NodeID); err != nil {
		return err
	}
	result.AutoMergeAction = "disabled"
	return nil
}

func selectPull(pulls []pullRequest) (*pullRequest, string, error) {
	var open *pullRequest
	ownedHeadSHA := ""
	for index := range pulls {
		pull := &pulls[index]
		managed := strings.Contains(pull.Body, managedMarker)
		if managed && ownedHeadSHA == "" {
			ownedHeadSHA = pull.Head.SHA
		}
		if pull.State != "open" {
			continue
		}
		if open != nil {
			return nil, "", errors.New("multiple open pull requests use the managed update branch")
		}
		if !managed {
			return nil, "", fmt.Errorf("refusing to manage pull request #%d without the HooNeedsUpdates marker", pull.Number)
		}
		open = pull
	}
	return open, ownedHeadSHA, nil
}

func autoMergeDecision(settings config.Automation, repository repository, report update.Report) (bool, string) {
	policy := settings.AutoMerge
	if !policy.Enabled {
		return false, "disabled by configuration"
	}
	if !repository.AllowAutoMerge {
		return false, "repository does not allow native GitHub auto-merge"
	}
	if report.Summary.Outdated > policy.MaxUpdates {
		return false, fmt.Sprintf("%d updates exceed maxUpdates %d", report.Summary.Outdated, policy.MaxUpdates)
	}
	allowedTypes := stringSet(policy.UpdateTypes)
	allowedManagers := stringSet(policy.Managers)
	patterns := make([]*regexp.Regexp, 0, len(policy.Dependencies))
	for _, pattern := range policy.Dependencies {
		patterns = append(patterns, regexp.MustCompile(pattern))
	}
	for _, entry := range report.Updates {
		if entry.Status != "outdated" {
			continue
		}
		if !allowedTypes[entry.UpdateType] {
			return false, fmt.Sprintf("%s update for %s is not allowed", entry.UpdateType, entry.Name)
		}
		if len(allowedManagers) > 0 && !allowedManagers[string(entry.Manager)] {
			return false, fmt.Sprintf("manager %s for %s is not allowed", entry.Manager, entry.Name)
		}
		if len(patterns) > 0 && !matchesAny(patterns, entry.Name) {
			return false, fmt.Sprintf("dependency %s is not allowed", entry.Name)
		}
	}
	return true, "all updates satisfy auto-merge policy; GitHub still requires configured checks and reviews"
}

func pullBody(name string, result Result, report update.Report, lockfiles bool) string {
	var body strings.Builder
	fmt.Fprintf(&body, "%s\n\n## HooNeedsUpdates\n\nAutomated update plan for `%s`.\n\n", managedMarker, markdown(name))
	body.WriteString("| Manager | Dependency | Current | Latest | Update |\n")
	body.WriteString("| --- | --- | --- | --- | --- |\n")
	shown := 0
	for _, entry := range report.Updates {
		if entry.Status != "outdated" {
			continue
		}
		if shown == 100 {
			fmt.Fprintf(&body, "\n%d additional updates omitted from this table.\n", report.Summary.Outdated-shown)
			break
		}
		fmt.Fprintf(&body, "| %s | `%s` | `%s` | `%s` | %s |\n",
			markdown(string(entry.Manager)), markdown(entry.Name), markdown(entry.CurrentVersion),
			markdown(entry.LatestVersion), markdown(entry.UpdateType))
		shown++
	}
	fmt.Fprintf(&body, "\n- Base: `%s@%s`\n", markdown(result.BaseBranch), result.BaseSHA)
	fmt.Fprintf(&body, "- Plan: `%s`\n", markdown(result.PlanDigest))
	if lockfiles {
		body.WriteString("- Lockfiles: applied twice in isolated worktrees; byte-identical output required\n")
	} else {
		body.WriteString("- Lockfiles: disabled; manifest edits only\n")
	}
	if result.AutoMergeEligible {
		body.WriteString("- Auto-merge: eligible; native GitHub auto-merge still waits for repository checks and reviews\n")
	} else {
		fmt.Fprintf(&body, "- Auto-merge: manual (%s)\n", markdown(result.AutoMergeReason))
	}
	body.WriteString("\nGenerated by HooNeedsUpdates. Review CI output before merge.\n")
	return body.String()
}

func pullMatches(pull pullRequest, title, body, base string) bool {
	return pull.Title == title && pull.Body == body && pull.Base.Ref == base
}

func setPullResult(result *Result, pull pullRequest) {
	result.PullRequestNumber = pull.Number
	result.PullRequestURL = pull.HTMLURL
}

func plannedAutoMergeAction(pull pullRequest, eligible bool, method string) string {
	if eligible && pull.AutoMerge != nil && !strings.EqualFold(pull.AutoMerge.MergeMethod, method) {
		return "would-reconfigure"
	}
	if eligible && pull.AutoMerge == nil {
		return "would-enable"
	}
	if !eligible && pull.AutoMerge != nil {
		return "would-disable"
	}
	if pull.AutoMerge != nil {
		return "already-enabled"
	}
	return "not-eligible"
}

func uniqueRepositories(values []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		normalized := strings.ToLower(value)
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func matchesAny(patterns []*regexp.Regexp, value string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func markdown(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "`", "\\`")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}

var _ host = (*githubHost)(nil)
var _ vcs = gitVCS{}
