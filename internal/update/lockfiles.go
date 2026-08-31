package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	maxCommandOutput   = 1 << 20
	maxGeneratedFile   = 64 << 20
	defaultCommandTime = 5 * time.Minute
)

type lockfileGroup struct {
	manager   Manager
	directory string
	tool      string
	manifests map[string]bool
	lockfiles map[string]bool
	optional  map[string]bool
	updates   []Update
	projects  map[string]bool
}

type regenerationPlan struct {
	manifests []plannedFile
	groups    []*lockfileGroup
	allowed   map[string]bool
	locks     map[string]bool
	required  map[string]bool
	source    map[string]sourceSnapshot
}

type sourceSnapshot struct {
	data    []byte
	existed bool
	mode    os.FileMode
}

type lockCommand struct {
	manager         Manager
	dir             string
	name            string
	args            []string
	env             []string
	outputPath      string
	destinationPath string
}

type commandRunner interface {
	Run(context.Context, lockCommand) ([]byte, error)
}

type executableRunner struct{}

// ApplyWithLockfiles applies the reviewed manifest plan in two independent,
// detached Git worktrees. Trusted package-manager commands regenerate only the
// expected lockfiles. Both runs must produce byte-identical files before any
// source-worktree write is allowed.
func ApplyWithLockfiles(
	ctx context.Context,
	root string,
	report Report,
	write bool,
	commandTimeout time.Duration,
) ([]AppliedFile, error) {
	return applyWithLockfiles(ctx, root, report, write, commandTimeout, executableRunner{})
}

func applyWithLockfiles(
	ctx context.Context,
	root string,
	report Report,
	write bool,
	commandTimeout time.Duration,
	runner commandRunner,
) ([]AppliedFile, error) {
	absRoot, err := repositoryRoot(root)
	if err != nil {
		return nil, err
	}
	if err := validateReport(absRoot, report); err != nil {
		return nil, err
	}
	if report.SchemaVersion != 2 || report.PlanDigest == "" {
		return nil, errors.New("lockfile mode requires a schema 2 report with a plan digest")
	}
	manifestPlans, err := planReport(absRoot, report)
	if err != nil {
		return nil, err
	}
	if len(manifestPlans) == 0 {
		return nil, nil
	}
	plan, err := buildRegenerationPlan(absRoot, report, manifestPlans)
	if err != nil {
		return nil, err
	}
	plan.source, err = snapshotSourceState(absRoot, plan)
	if err != nil {
		return nil, err
	}
	if err := validateSourceState(absRoot, plan); err != nil {
		return nil, err
	}
	if err := validateSourceSnapshot(absRoot, plan.source); err != nil {
		return nil, err
	}
	if commandTimeout <= 0 {
		commandTimeout = defaultCommandTime
	}
	operationRoot, err := os.MkdirTemp("", "hooneedsupdates-lockfiles-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(operationRoot)
	if err := os.Mkdir(filepath.Join(operationRoot, "hooks"), 0o700); err != nil {
		return nil, err
	}

	first, err := regenerateOnce(ctx, absRoot, report, plan, operationRoot, "first", commandTimeout, runner)
	if err != nil {
		return nil, err
	}
	second, err := regenerateOnce(ctx, absRoot, report, plan, operationRoot, "second", commandTimeout, runner)
	if err != nil {
		return nil, err
	}
	if err := compareRegenerations(first, second); err != nil {
		return nil, err
	}
	result := make([]AppliedFile, 0, len(first))
	for _, file := range first {
		result = append(result, file.file)
	}
	if write {
		if err := writePlans(first); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (executableRunner) Run(ctx context.Context, command lockCommand) ([]byte, error) {
	executable, err := exec.LookPath(command.name)
	if err != nil {
		return nil, fmt.Errorf("required %s executable %q: %w", command.manager, command.name, err)
	}
	process := exec.CommandContext(ctx, executable, command.args...)
	process.Dir = command.dir
	process.Env = command.env
	output := &limitedBuffer{remaining: maxCommandOutput}
	process.Stdout = output
	process.Stderr = output
	if err := process.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		if message == "" {
			return nil, fmt.Errorf("%s command failed: %w", command.manager, err)
		}
		return nil, fmt.Errorf("%s command failed: %w: %s", command.manager, err, message)
	}
	if command.outputPath != "" {
		if err := copyGeneratedOutput(command.outputPath, command.destinationPath); err != nil {
			return nil, fmt.Errorf("collect %s command output: %w", command.manager, err)
		}
	}
	return output.Bytes(), nil
}

func copyGeneratedOutput(source, destination string) error {
	data, exists, _, err := readOptionalRegular(source, source)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("expected output %s was not generated", source)
	}
	mode := os.FileMode(0o644)
	if _, destinationExists, destinationMode, err := readOptionalRegular(destination, destination); err != nil {
		return err
	} else if destinationExists {
		mode = destinationMode
	}
	return atomicWrite(destination, data, mode)
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.remaining {
		written := b.remaining
		if b.remaining > 0 {
			_, _ = b.buffer.Write(data[:b.remaining])
			b.remaining = 0
		}
		return written, errors.New("command output exceeded 1 MiB")
	}
	b.remaining -= len(data)
	return b.buffer.Write(data)
}

func (b *limitedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *limitedBuffer) String() string { return b.buffer.String() }

func buildRegenerationPlan(root string, report Report, manifests []plannedFile) (regenerationPlan, error) {
	plan := regenerationPlan{
		manifests: manifests,
		allowed:   map[string]bool{},
		locks:     map[string]bool{},
		required:  map[string]bool{},
	}
	manifestByPath := map[string]plannedFile{}
	for _, manifest := range manifests {
		manifestByPath[manifest.file.Path] = manifest
		plan.allowed[manifest.file.Path] = true
	}
	groups := map[string]*lockfileGroup{}
	for _, entry := range report.Updates {
		if entry.Status != "outdated" || !lockfileManager(entry.Manager) {
			continue
		}
		if _, ok := manifestByPath[entry.File]; !ok {
			return regenerationPlan{}, fmt.Errorf("missing manifest plan for %s", entry.File)
		}
		var additions []*lockfileGroup
		var err error
		switch entry.Manager {
		case ManagerGoMod:
			additions, err = goGroups(root, entry)
		case ManagerCargo:
			additions, err = cargoGroups(root, entry)
		case ManagerNPM:
			additions, err = npmGroups(root, entry)
		case ManagerNuGet:
			additions, err = nugetGroups(root, entry)
		}
		if err != nil {
			return regenerationPlan{}, err
		}
		for _, addition := range additions {
			key := string(addition.manager) + "\x00" + addition.directory + "\x00" + addition.tool
			group := groups[key]
			if group == nil {
				group = &lockfileGroup{
					manager: addition.manager, directory: addition.directory, tool: addition.tool,
					manifests: map[string]bool{}, lockfiles: map[string]bool{}, optional: map[string]bool{}, projects: map[string]bool{},
				}
				groups[key] = group
			}
			group.updates = append(group.updates, entry)
			mergePaths(group.manifests, addition.manifests)
			mergePaths(group.lockfiles, addition.lockfiles)
			mergePaths(group.optional, addition.optional)
			mergePaths(group.projects, addition.projects)
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := groups[key]
		sort.SliceStable(group.updates, func(i, j int) bool {
			if group.updates[i].File != group.updates[j].File {
				return group.updates[i].File < group.updates[j].File
			}
			if group.updates[i].Name != group.updates[j].Name {
				return group.updates[i].Name < group.updates[j].Name
			}
			return group.updates[i].Line < group.updates[j].Line
		})
		for path := range group.lockfiles {
			plan.allowed[path] = true
			plan.locks[path] = true
			if !group.optional[path] {
				plan.required[path] = true
			}
		}
		plan.groups = append(plan.groups, group)
	}
	return plan, nil
}

func lockfileManager(manager Manager) bool {
	switch manager {
	case ManagerGoMod, ManagerCargo, ManagerNPM, ManagerNuGet:
		return true
	default:
		return false
	}
}

func mergePaths(destination, source map[string]bool) {
	for path := range source {
		destination[path] = true
	}
}

func newLockfileGroup(manager Manager, directory, tool string) *lockfileGroup {
	return &lockfileGroup{
		manager: manager, directory: directory, tool: tool,
		manifests: map[string]bool{}, lockfiles: map[string]bool{}, optional: map[string]bool{}, projects: map[string]bool{},
	}
}

func goGroups(root string, entry Update) ([]*lockfileGroup, error) {
	directory := cleanRelativeDirectory(filepath.Dir(filepath.FromSlash(entry.File)))
	group := newLockfileGroup(ManagerGoMod, directory, "go")
	group.manifests[entry.File] = true
	group.lockfiles[joinRelative(directory, "go.sum")] = true
	if workspace, ok, err := nearestFile(root, directory, "go.work"); err != nil {
		return nil, err
	} else if ok {
		workspaceSum := joinRelative(filepath.Dir(filepath.FromSlash(workspace)), "go.work.sum")
		group.lockfiles[workspaceSum] = true
		group.optional[workspaceSum] = true
	}
	return []*lockfileGroup{group}, nil
}

func cargoGroups(root string, entry Update) ([]*lockfileGroup, error) {
	directory, err := cargoWorkspace(root, entry.File)
	if err != nil {
		return nil, err
	}
	group := newLockfileGroup(ManagerCargo, directory, "cargo")
	group.manifests[entry.File] = true
	group.lockfiles[joinRelative(directory, "Cargo.lock")] = true
	return []*lockfileGroup{group}, nil
}

func npmGroups(root string, entry Update) ([]*lockfileGroup, error) {
	directory, tool, lockfile, err := npmLockfile(root, entry.File)
	if err != nil {
		return nil, err
	}
	group := newLockfileGroup(ManagerNPM, directory, tool)
	group.manifests[entry.File] = true
	group.lockfiles[lockfile] = true
	return []*lockfileGroup{group}, nil
}

func nugetGroups(root string, entry Update) ([]*lockfileGroup, error) {
	base := filepath.Base(filepath.FromSlash(entry.File))
	var projects []string
	if strings.EqualFold(filepath.Ext(base), ".csproj") {
		projects = []string{entry.File}
	} else if base == "Directory.Packages.props" {
		var err error
		projects, err = nugetProjects(root, cleanRelativeDirectory(filepath.Dir(filepath.FromSlash(entry.File))))
		if err != nil {
			return nil, err
		}
		if len(projects) == 0 {
			return nil, fmt.Errorf("%s has no descendant .csproj files", entry.File)
		}
	} else {
		return nil, fmt.Errorf("unsupported NuGet manifest %s", entry.File)
	}
	groups := make([]*lockfileGroup, 0, len(projects))
	for _, project := range projects {
		directory := cleanRelativeDirectory(filepath.Dir(filepath.FromSlash(project)))
		group := newLockfileGroup(ManagerNuGet, directory, "dotnet")
		group.manifests[entry.File] = true
		group.projects[project] = true
		group.lockfiles[joinRelative(directory, "packages.lock.json")] = true
		groups = append(groups, group)
	}
	return groups, nil
}

func cleanRelativeDirectory(directory string) string {
	directory = filepath.Clean(directory)
	if directory == "." || directory == string(filepath.Separator) {
		return "."
	}
	return filepath.ToSlash(directory)
}

func joinRelative(directory, name string) string {
	if directory == "." || directory == "" {
		return name
	}
	return filepath.ToSlash(filepath.Join(filepath.FromSlash(directory), name))
}

func nearestFile(root, startDirectory, name string) (string, bool, error) {
	root = filepath.Clean(root)
	directory := filepath.Join(root, filepath.FromSlash(startDirectory))
	for {
		candidate := filepath.Join(directory, name)
		info, err := os.Lstat(candidate)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return "", false, fmt.Errorf("refusing non-regular %s", candidate)
			}
			relative, err := filepath.Rel(root, candidate)
			if err != nil {
				return "", false, err
			}
			return filepath.ToSlash(relative), true, nil
		case !errors.Is(err, os.ErrNotExist):
			return "", false, err
		}
		if directory == root {
			return "", false, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory || !pathWithin(root, parent) {
			return "", false, nil
		}
		directory = parent
	}
}

func cargoWorkspace(root, manifest string) (string, error) {
	start := cleanRelativeDirectory(filepath.Dir(filepath.FromSlash(manifest)))
	if lockfile, ok, err := nearestFile(root, start, "Cargo.lock"); err != nil {
		return "", err
	} else if ok {
		return cleanRelativeDirectory(filepath.Dir(filepath.FromSlash(lockfile))), nil
	}
	root = filepath.Clean(root)
	directory := filepath.Join(root, filepath.FromSlash(start))
	for {
		manifestPath := filepath.Join(directory, "Cargo.toml")
		data, err := os.ReadFile(manifestPath)
		if err == nil && hasCargoWorkspace(data) {
			relative, err := filepath.Rel(root, directory)
			if err != nil {
				return "", err
			}
			return cleanRelativeDirectory(relative), nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if directory == root {
			break
		}
		directory = filepath.Dir(directory)
	}
	return start, nil
}

func hasCargoWorkspace(data []byte) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "[workspace]" || strings.HasPrefix(trimmed, "[workspace] #") {
			return true
		}
	}
	return false
}

func npmLockfile(root, manifest string) (directory, tool, lockfile string, err error) {
	start := cleanRelativeDirectory(filepath.Dir(filepath.FromSlash(manifest)))
	root = filepath.Clean(root)
	current := filepath.Join(root, filepath.FromSlash(start))
	for {
		var matches []struct {
			name string
			tool string
		}
		for _, candidate := range []struct {
			name string
			tool string
		}{{"bun.lock", "bun"}, {"bun.lockb", "bun"}, {"package-lock.json", "npm"}} {
			info, statErr := os.Lstat(filepath.Join(current, candidate.name))
			if statErr == nil {
				if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
					return "", "", "", fmt.Errorf("refusing non-regular %s", filepath.Join(current, candidate.name))
				}
				matches = append(matches, candidate)
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return "", "", "", statErr
			}
		}
		if len(matches) > 1 {
			return "", "", "", fmt.Errorf("multiple JavaScript lockfiles beside or above %s", manifest)
		}
		if len(matches) == 1 {
			relativeDirectory, relErr := filepath.Rel(root, current)
			if relErr != nil {
				return "", "", "", relErr
			}
			directory = cleanRelativeDirectory(relativeDirectory)
			return directory, matches[0].tool, joinRelative(directory, matches[0].name), nil
		}
		if current == root {
			break
		}
		current = filepath.Dir(current)
	}
	data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(manifest)))
	if readErr != nil {
		return "", "", "", readErr
	}
	var metadata struct {
		PackageManager string `json:"packageManager"`
	}
	if unmarshalErr := json.Unmarshal(data, &metadata); unmarshalErr != nil {
		return "", "", "", unmarshalErr
	}
	directory = start
	switch {
	case strings.HasPrefix(metadata.PackageManager, "bun@"):
		return directory, "bun", joinRelative(directory, "bun.lock"), nil
	case strings.HasPrefix(metadata.PackageManager, "npm@"):
		return directory, "npm", joinRelative(directory, "package-lock.json"), nil
	default:
		return "", "", "", fmt.Errorf("%s has no supported lockfile or bun/npm packageManager", manifest)
	}
}

func nugetProjects(root, directory string) ([]string, error) {
	start, err := containedPath(root, joinRelative(directory, "."))
	if err != nil {
		return nil, err
	}
	var projects []string
	err = filepath.WalkDir(start, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != start && excludedDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".csproj") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		projects = append(projects, filepath.ToSlash(relative))
		return nil
	})
	sort.Strings(projects)
	return projects, err
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func repositoryRoot(root string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	output, err := runGit(context.Background(), absRoot, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("lockfile mode requires a Git repository: %w", err)
	}
	repository := filepath.Clean(strings.TrimSpace(string(output)))
	if repository != filepath.Clean(absRoot) {
		return "", fmt.Errorf("lockfile mode requires repository root %q, got %q", repository, absRoot)
	}
	return repository, nil
}

func validateSourceState(root string, plan regenerationPlan) error {
	if err := rejectRepositoryGitContentFilters(root); err != nil {
		return err
	}
	if err := rejectCargoProjectConfig(root, plan.groups); err != nil {
		return err
	}
	paths := make([]string, 0, len(plan.allowed))
	for path := range plan.allowed {
		fullPath, err := containedPath(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(fullPath)
		if errors.Is(err, os.ErrNotExist) && plan.locks[path] {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular planned file %s", path)
		}
		if _, err := runGit(context.Background(), root, "", "ls-files", "--error-unmatch", "--", path); err != nil {
			return fmt.Errorf("planned file %s must be tracked: %w", path, err)
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil
	}
	arguments := append([]string{"diff", "--quiet", "HEAD", "--"}, paths...)
	if _, err := runGit(context.Background(), root, "", arguments...); err != nil {
		if exitCodeIs(err, 1) {
			return errors.New("planned manifests or lockfiles have uncommitted changes")
		}
		return fmt.Errorf("inspect planned files: %w", err)
	}
	return nil
}

func rejectRepositoryGitContentFilters(root string) error {
	scopes := []string{"--local"}
	worktreeConfig, err := runGit(context.Background(), root, "", "config", "--local", "--bool", "--get", "extensions.worktreeConfig")
	if err == nil && strings.TrimSpace(string(worktreeConfig)) == "true" {
		scopes = append(scopes, "--worktree")
	} else if err != nil && !exitCodeIs(err, 1) {
		return fmt.Errorf("inspect Git worktree configuration: %w", err)
	}
	for _, scope := range scopes {
		output, err := runGit(context.Background(), root, "", "config", scope, "--show-origin", "--get-regexp", `^filter\..*\.(clean|smudge|process)$`)
		if err == nil {
			return fmt.Errorf("Git content filters are not allowed in repository configuration: %s", strings.TrimSpace(string(output)))
		}
		if !exitCodeIs(err, 1) {
			return fmt.Errorf("inspect Git content filters: %w", err)
		}
	}
	return nil
}

func snapshotSourceState(root string, plan regenerationPlan) (map[string]sourceSnapshot, error) {
	snapshots := make(map[string]sourceSnapshot, len(plan.allowed))
	for _, relative := range sortedPaths(plan.allowed) {
		path, err := containedPath(root, relative)
		if err != nil {
			return nil, err
		}
		data, existed, mode, err := readOptionalRegular(path, relative)
		if err != nil {
			return nil, err
		}
		if !existed && !plan.locks[relative] {
			return nil, fmt.Errorf("planned manifest %s disappeared", relative)
		}
		snapshots[relative] = sourceSnapshot{data: data, existed: existed, mode: mode}
	}
	for _, manifest := range plan.manifests {
		snapshot := snapshots[manifest.file.Path]
		if !snapshot.existed || !bytes.Equal(snapshot.data, manifest.file.Before) {
			return nil, fmt.Errorf("planned manifest %s changed after planning", manifest.file.Path)
		}
	}
	return snapshots, nil
}

func validateSourceSnapshot(root string, snapshots map[string]sourceSnapshot) error {
	for _, relative := range sortedSnapshotPaths(snapshots) {
		path, err := containedPath(root, relative)
		if err != nil {
			return err
		}
		current, existed, mode, err := readOptionalRegular(path, relative)
		if err != nil {
			return err
		}
		expected := snapshots[relative]
		if existed != expected.existed || mode != expected.mode || !bytes.Equal(current, expected.data) {
			return fmt.Errorf("planned file %s changed while source state was validated", relative)
		}
	}
	return nil
}

func sortedSnapshotPaths(snapshots map[string]sourceSnapshot) []string {
	result := make([]string, 0, len(snapshots))
	for path := range snapshots {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func rejectCargoProjectConfig(root string, groups []*lockfileGroup) error {
	checked := map[string]bool{}
	for _, group := range groups {
		if group.manager != ManagerCargo {
			continue
		}
		directory := filepath.Join(root, filepath.FromSlash(group.directory))
		for {
			for _, name := range []string{"config.toml", "config"} {
				candidate := filepath.Join(directory, ".cargo", name)
				if checked[candidate] {
					continue
				}
				checked[candidate] = true
				if _, err := os.Lstat(candidate); err == nil {
					return fmt.Errorf("repository Cargo configuration is not allowed in lockfile mode: %s", candidate)
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			if filepath.Clean(directory) == filepath.Clean(root) {
				break
			}
			parent := filepath.Dir(directory)
			if parent == directory || !pathWithin(root, parent) {
				break
			}
			directory = parent
		}
	}
	return nil
}

func runGit(ctx context.Context, directory, hooksDirectory string, arguments ...string) ([]byte, error) {
	executable, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	base := []string{"-c", "core.fsmonitor=false"}
	if hooksDirectory != "" {
		base = append(base, "-c", "core.hooksPath="+hooksDirectory)
	}
	base = append(base, "-C", directory)
	base = append(base, arguments...)
	process := exec.CommandContext(ctx, executable, base...)
	output := &limitedBuffer{remaining: maxCommandOutput}
	process.Stdout = output
	process.Stderr = output
	err = process.Run()
	if err != nil {
		message := strings.TrimSpace(output.String())
		if message != "" {
			return output.Bytes(), fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, message)
		}
		return output.Bytes(), fmt.Errorf("git %s: %w", strings.Join(arguments, " "), err)
	}
	return output.Bytes(), nil
}

func exitCodeIs(err error, wanted int) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError) && exitError.ExitCode() == wanted
}

func regenerateOnce(
	ctx context.Context,
	sourceRoot string,
	report Report,
	plan regenerationPlan,
	operationRoot string,
	label string,
	commandTimeout time.Duration,
	runner commandRunner,
) ([]plannedFile, error) {
	worktree := filepath.Join(operationRoot, label+"-worktree")
	cacheRoot := filepath.Join(operationRoot, label+"-cache")
	hooks := filepath.Join(operationRoot, "hooks")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		return nil, err
	}
	if _, err := runGit(ctx, sourceRoot, hooks, "worktree", "add", "--detach", worktree, "HEAD"); err != nil {
		return nil, fmt.Errorf("create %s regeneration worktree: %w", label, err)
	}
	defer func() {
		_, _ = runGit(context.Background(), sourceRoot, hooks, "worktree", "remove", "--force", worktree)
	}()
	worktreeReport := report
	worktreeReport.Root = filepath.ToSlash(worktree)
	if _, err := Apply(worktree, worktreeReport, true); err != nil {
		return nil, fmt.Errorf("apply manifests in %s regeneration: %w", label, err)
	}
	cargoPins, err := pinCargoManifests(worktree, plan)
	if err != nil {
		return nil, fmt.Errorf("pin Cargo manifests in %s regeneration: %w", label, err)
	}
	for _, group := range plan.groups {
		commands, err := commandsForGroup(worktree, cacheRoot, group)
		if err != nil {
			return nil, err
		}
		for _, command := range commands {
			commandContext, cancel := context.WithTimeout(ctx, commandTimeout)
			_, commandErr := runner.Run(commandContext, command)
			cancel()
			if commandErr != nil {
				return nil, fmt.Errorf("regenerate %s lockfile in %s: %w", group.manager, group.directory, commandErr)
			}
		}
	}
	if err := restoreCargoManifests(cargoPins); err != nil {
		return nil, fmt.Errorf("restore approved Cargo manifests in %s regeneration: %w", label, err)
	}
	generated, err := collectRegeneration(sourceRoot, worktree, plan)
	if err != nil {
		return nil, fmt.Errorf("validate %s regeneration: %w", label, err)
	}
	return generated, nil
}

func commandsForGroup(worktree, cacheRoot string, group *lockfileGroup) ([]lockCommand, error) {
	directory, err := containedPath(worktree, joinRelative(group.directory, "."))
	if err != nil {
		return nil, err
	}
	environment, err := managerEnvironment(cacheRoot)
	if err != nil {
		return nil, err
	}
	command := func(name string, arguments ...string) lockCommand {
		return lockCommand{manager: group.manager, dir: directory, name: name, args: arguments, env: environment}
	}
	switch group.manager {
	case ManagerGoMod:
		return []lockCommand{command("go", "mod", "tidy")}, nil
	case ManagerCargo:
		var commands []lockCommand
		seen := map[string]bool{}
		for _, entry := range group.updates {
			key := entry.File + "\x00" + entry.Name + "\x00" + entry.LatestVersion
			if seen[key] {
				continue
			}
			seen[key] = true
			manifest, err := containedPath(worktree, entry.File)
			if err != nil {
				return nil, err
			}
			precise := strings.TrimPrefix(entry.LatestVersion, "v")
			commands = append(commands, command(
				"cargo", "--config", "net.git-fetch-with-cli=false",
				"--config", `registry.global-credential-providers=["cargo:token"]`,
				"update", "--manifest-path", manifest, "--package", entry.Name, "--precise", precise,
			))
		}
		return commands, nil
	case ManagerNPM:
		switch group.tool {
		case "bun":
			return []lockCommand{command(
				"bun", "install", "--lockfile-only", "--ignore-scripts", "--no-progress", "--no-summary",
				"--cache-dir", filepath.Join(cacheRoot, "bun"),
			)}, nil
		case "npm":
			return []lockCommand{command(
				"npm", "install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund",
				"--allow-git=none", "--cache", filepath.Join(cacheRoot, "npm"),
			)}, nil
		default:
			return nil, fmt.Errorf("unsupported JavaScript lockfile tool %q", group.tool)
		}
	case ManagerNuGet:
		projects := sortedPaths(group.projects)
		if len(projects) > 1 {
			return nil, fmt.Errorf("multiple NuGet projects share lockfile %s", joinRelative(group.directory, "packages.lock.json"))
		}
		commands := make([]lockCommand, 0, len(projects))
		for _, project := range projects {
			synthetic, generatedLock, configFile, err := sanitizedNuGetProject(worktree, cacheRoot, project)
			if err != nil {
				return nil, err
			}
			destination, err := containedPath(worktree, joinRelative(group.directory, "packages.lock.json"))
			if err != nil {
				return nil, err
			}
			artifactPath := filepath.Join(cacheRoot, "dotnet-artifacts", strings.ReplaceAll(project, "/", "_"))
			lockCommand := command(
				"dotnet", "restore", synthetic, "--use-lock-file", "--force-evaluate", "--no-http-cache",
				"--disable-build-servers", "--no-dependencies", "--artifacts-path", artifactPath,
				"--packages", filepath.Join(cacheRoot, "nuget"), "--configfile", configFile,
				"--lock-file-path", generatedLock, "--verbosity", "quiet", "-p:NuGetAudit=false",
			)
			lockCommand.outputPath = generatedLock
			lockCommand.destinationPath = destination
			commands = append(commands, lockCommand)
		}
		return commands, nil
	default:
		return nil, fmt.Errorf("unsupported lockfile manager %q", group.manager)
	}
}

func managerEnvironment(cacheRoot string) ([]string, error) {
	directories := []string{
		filepath.Join(cacheRoot, "home"),
		filepath.Join(cacheRoot, "tmp"),
		filepath.Join(cacheRoot, "xdg"),
		filepath.Join(cacheRoot, "go-mod"),
		filepath.Join(cacheRoot, "go-build"),
		filepath.Join(cacheRoot, "cargo"),
		filepath.Join(cacheRoot, "nuget"),
		filepath.Join(cacheRoot, "dotnet-home"),
	}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(cacheRoot, "home"),
		"TMPDIR=" + filepath.Join(cacheRoot, "tmp"),
		"XDG_CACHE_HOME=" + filepath.Join(cacheRoot, "xdg"),
		"GIT_TERMINAL_PROMPT=0",
		"CI=1",
		"GOTOOLCHAIN=local",
		"GOMODCACHE=" + filepath.Join(cacheRoot, "go-mod"),
		"GOCACHE=" + filepath.Join(cacheRoot, "go-build"),
		"CARGO_HOME=" + filepath.Join(cacheRoot, "cargo"),
		"NUGET_PACKAGES=" + filepath.Join(cacheRoot, "nuget"),
		"DOTNET_CLI_HOME=" + filepath.Join(cacheRoot, "dotnet-home"),
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1",
		"DOTNET_NOLOGO=1",
		"NUGET_XMLDOC_MODE=skip",
	}
	rustupHome := os.Getenv("RUSTUP_HOME")
	if rustupHome == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			rustupHome = filepath.Join(userHome, ".rustup")
		}
	}
	if info, err := os.Stat(rustupHome); err == nil && info.IsDir() {
		environment = append(environment, "RUSTUP_HOME="+rustupHome)
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{"SystemRoot", "ComSpec", "PATHEXT", "WINDIR"} {
			if value := os.Getenv(name); value != "" {
				environment = append(environment, name+"="+value)
			}
		}
		environment = append(environment,
			"TEMP="+filepath.Join(cacheRoot, "tmp"),
			"TMP="+filepath.Join(cacheRoot, "tmp"),
		)
	}
	return environment, nil
}

func sortedPaths(paths map[string]bool) []string {
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func collectRegeneration(sourceRoot, worktree string, plan regenerationPlan) ([]plannedFile, error) {
	manifestExpected := map[string]plannedFile{}
	for _, manifest := range plan.manifests {
		manifestExpected[manifest.file.Path] = manifest
		generatedPath, err := containedPath(worktree, manifest.file.Path)
		if err != nil {
			return nil, err
		}
		generated, err := os.ReadFile(generatedPath)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(generated, manifest.file.After) {
			return nil, fmt.Errorf("package manager changed approved manifest %s", manifest.file.Path)
		}
	}
	for lockfile := range plan.required {
		path, err := containedPath(worktree, lockfile)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("required lockfile %s was not generated: %w", lockfile, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("generated lockfile %s is not regular", lockfile)
		}
	}
	changed, err := changedPaths(worktree)
	if err != nil {
		return nil, err
	}
	for path := range changed {
		if !plan.allowed[path] {
			return nil, fmt.Errorf("package manager changed unexpected path %s", path)
		}
	}
	for path := range plan.allowed {
		different, err := filesDifferSnapshot(worktree, path, plan.source[path])
		if err != nil {
			return nil, err
		}
		if different {
			changed[path] = true
		}
	}
	paths := sortedPaths(changed)
	generated := make([]plannedFile, 0, len(paths))
	for _, relative := range paths {
		if !plan.allowed[relative] {
			return nil, fmt.Errorf("unexpected generated path %s", relative)
		}
		worktreePath, err := containedPath(worktree, relative)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(worktreePath)
		if err != nil {
			return nil, fmt.Errorf("generated path %s disappeared: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("generated path %s is not a regular file", relative)
		}
		if info.Size() > maxGeneratedFile {
			return nil, fmt.Errorf("generated path %s exceeds 64 MiB", relative)
		}
		after, err := os.ReadFile(worktreePath)
		if err != nil {
			return nil, err
		}
		sourcePath, err := containedPath(sourceRoot, relative)
		if err != nil {
			return nil, err
		}
		snapshot := plan.source[relative]
		before, existed, sourceMode := snapshot.data, snapshot.existed, snapshot.mode
		if bytes.Equal(before, after) && existed {
			continue
		}
		kind := "lockfile"
		updates := 0
		if manifest, ok := manifestExpected[relative]; ok {
			kind = "manifest"
			updates = manifest.file.Updates
		}
		mode := info.Mode().Perm()
		if existed {
			mode = sourceMode
		}
		file := AppliedFile{
			Path: relative, Updates: updates, Kind: kind, Created: !existed,
			Before: before, After: after,
		}
		generated = append(generated, plannedFile{
			root: sourceRoot, path: sourcePath, mode: mode, existed: existed, file: file,
		})
	}
	return generated, nil
}

func changedPaths(worktree string) (map[string]bool, error) {
	result := map[string]bool{}
	commands := [][]string{
		{"diff", "--name-only", "-z", "HEAD", "--"},
		{"ls-files", "--others", "--exclude-standard", "-z"},
		{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"},
	}
	for _, arguments := range commands {
		output, err := runGit(context.Background(), worktree, "", arguments...)
		if err != nil {
			return nil, err
		}
		for _, path := range bytes.Split(output, []byte{0}) {
			if len(path) == 0 {
				continue
			}
			relative := filepath.ToSlash(string(path))
			if relative == ".git" || strings.HasPrefix(relative, ".git/") {
				continue
			}
			result[relative] = true
		}
	}
	return result, nil
}

func filesDifferSnapshot(worktree, relative string, source sourceSnapshot) (bool, error) {
	worktreePath, err := containedPath(worktree, relative)
	if err != nil {
		return false, err
	}
	generated, generatedExists, _, err := readOptionalRegular(worktreePath, relative)
	if err != nil {
		return false, err
	}
	if source.existed != generatedExists {
		return true, nil
	}
	return !bytes.Equal(source.data, generated), nil
}

func readOptionalRegular(path, relative string) ([]byte, bool, os.FileMode, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, 0o644, nil
	}
	if err != nil {
		return nil, false, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, 0, fmt.Errorf("refusing non-regular file %s", relative)
	}
	if info.Size() > maxGeneratedFile {
		return nil, false, 0, fmt.Errorf("file %s exceeds 64 MiB", relative)
	}
	data, err := os.ReadFile(path)
	return data, true, info.Mode().Perm(), err
}

func compareRegenerations(first, second []plannedFile) error {
	if len(first) != len(second) {
		return fmt.Errorf("lockfile regeneration is not reproducible: first run changed %d files, second changed %d", len(first), len(second))
	}
	for index := range first {
		left, right := first[index], second[index]
		if left.file.Path != right.file.Path {
			return fmt.Errorf("lockfile regeneration path set is not reproducible: %s != %s", left.file.Path, right.file.Path)
		}
		if !bytes.Equal(left.file.After, right.file.After) {
			return fmt.Errorf("lockfile regeneration is not byte-reproducible for %s", left.file.Path)
		}
		if left.mode != right.mode || left.file.Kind != right.file.Kind || left.file.Created != right.file.Created {
			return fmt.Errorf("lockfile regeneration metadata is not reproducible for %s", left.file.Path)
		}
	}
	return nil
}

var _ io.Writer = (*limitedBuffer)(nil)
