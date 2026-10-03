package update

import (
	"context"
	"fmt"
	"github.com/openhoo/hooneedsupdates/internal/config"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxManifestSize = 5 << 20

var (
	constraintToken = regexp.MustCompile(`(?i)(v?\d+(?:\.\d+){0,2}(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)`)

	simpleVersion    = regexp.MustCompile(`^[=~^]?v?\d+(?:\.\d+){0,2}(?:[-+][0-9A-Za-z.+-]+)?$`)
	manifestManagers = map[string]string{
		"go.mod":                   string(ManagerGoMod),
		"Cargo.toml":               string(ManagerCargo),
		"package.json":             string(ManagerNPM),
		"Directory.Packages.props": string(ManagerNuGet),
	}
	cargoSections = map[string]bool{
		"dependencies": true, "dev-dependencies": true,
		"build-dependencies": true, "workspace.dependencies": true,
	}
)

type Extractor struct {
	Root   string
	Config config.Config
}

func (e Extractor) Extract() ([]Candidate, error) {
	return e.ExtractContext(context.Background())
}

// ExtractContext stops directory traversal when the scan is canceled.
func (e Extractor) ExtractContext(ctx context.Context) ([]Candidate, error) {
	root, err := filepath.Abs(e.Root)
	if err != nil {
		return nil, err
	}
	// The explicitly selected root can be an alias; descendants remain subject
	// to the manifest symlink boundary enforced below.
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scan root must be a directory: %s", root)
	}
	var candidates []Candidate
	walk := e.walkEntry(root, &candidates)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return walk(path, entry, walkErr)
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].File != candidates[j].File {
			return candidates[i].File < candidates[j].File
		}
		if candidates[i].Line != candidates[j].Line {
			return candidates[i].Line < candidates[j].Line
		}
		return candidates[i].Name < candidates[j].Name
	})
	return deduplicate(candidates), nil
}

func (e Extractor) walkEntry(root string, candidates *[]Candidate) fs.WalkDirFunc {
	return func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel != "." && (excludedDirectory(entry.Name()) || e.Config.PathExcluded(rel) || e.Config.PathExcluded(rel+"/")) {
				return filepath.SkipDir
			}
			return nil
		}
		entries, err := e.extractFile(path, rel, entry)
		if err != nil {
			return err
		}
		*candidates = append(*candidates, entries...)
		return nil
	}
}

func (e Extractor) extractFile(path, rel string, entry fs.DirEntry) ([]Candidate, error) {
	if entry.Type()&os.ModeSymlink != 0 || e.Config.PathExcluded(rel) {
		return nil, nil
	}
	info, err := entry.Info()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	manager := managerFor(rel, e.Config)
	custom := matchingCustomManagers(rel, e.Config.CustomManagers)
	if manager == "" && len(custom) == 0 {
		return nil, nil
	}
	if info.Size() > maxManifestSize {
		return nil, fmt.Errorf("manifest %s exceeds 5 MiB limit", rel)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("manifest %s changed while opening", rel)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxManifestSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxManifestSize {
		return nil, fmt.Errorf("manifest %s exceeds 5 MiB limit", rel)
	}
	var result []Candidate
	if manager != "" {
		extracted, err := extractManager(Manager(manager), rel, data)
		if err != nil {
			return nil, fmt.Errorf("extract %s: %w", rel, err)
		}
		result = append(result, extracted...)
	}
	for _, customManager := range custom {
		extracted, err := extractCustom(rel, data, customManager)
		if err != nil {
			return nil, fmt.Errorf("extract custom manager %s from %s: %w", customManager.Name, rel, err)
		}
		result = append(result, extracted...)
	}
	return result, nil
}

func excludedDirectory(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "target", "dist", "bin", "obj", ".idea", ".vscode":
		return true
	default:
		return false
	}
}

func managerFor(rel string, cfg config.Config) string {
	base := pathpkg.Base(rel)
	manager := manifestManagers[base]
	if manager == "" {
		manager = specialManager(rel, base)
	}
	if manager != "" && cfg.ManagerEnabled(manager) {
		return manager
	}
	return ""
}

func specialManager(rel, base string) string {
	if strings.HasSuffix(base, ".csproj") {
		return string(ManagerNuGet)
	}
	if isWorkflow(rel) {
		return string(ManagerGitHubActions)
	}
	if strings.HasPrefix(base, "Dockerfile") {
		return string(ManagerDocker)
	}
	return ""
}

func isWorkflow(rel string) bool {
	base := pathpkg.Base(rel)
	if base != "action.yml" && base != "action.yaml" && !strings.HasPrefix(rel, ".github/workflows/") {
		return false
	}
	return strings.HasSuffix(rel, ".yml") || strings.HasSuffix(rel, ".yaml")
}

func extractManager(manager Manager, rel string, data []byte) ([]Candidate, error) {
	switch manager {
	case ManagerGoMod:
		return extractGoMod(rel, data)
	case ManagerCargo:
		return extractCargo(rel, data), nil
	case ManagerNPM:
		return extractNPM(rel, data)
	case ManagerNuGet:
		return extractNuGet(rel, data)
	case ManagerGitHubActions:
		return extractActions(rel, data)
	case ManagerDocker:
		return extractDocker(rel, data), nil
	default:
		return nil, fmt.Errorf("unsupported manager %q", manager)
	}
}

func matchingCustomManagers(rel string, managers []config.CustomManager) []config.CustomManager {
	var result []config.CustomManager
	for _, manager := range managers {
		for _, pattern := range manager.FilePatterns {
			compiled, err := regexp.Compile(pattern)
			if err == nil && compiled.MatchString(rel) {
				result = append(result, manager)
				break
			}
		}
	}
	return result
}

func extractCustom(rel string, data []byte, manager config.CustomManager) ([]Candidate, error) {
	var result []Candidate
	for _, expression := range manager.MatchStrings {
		pattern, err := regexp.Compile(expression)
		if err != nil {
			return nil, err
		}
		valueIndex := pattern.SubexpIndex("currentValue")
		for _, match := range pattern.FindAllSubmatchIndex(data, -1) {
			start, end := match[valueIndex*2], match[valueIndex*2+1]
			if start < 0 {
				continue
			}
			value := string(data[start:end])
			result = append(result, candidate(ManagerCustom, manager.Datasource, manager.DependencyName, value, rel, data, byteRange{start, end}))
		}
	}
	return result, nil
}

func candidate(manager Manager, datasource, name, version, rel string, data []byte, span byteRange) Candidate {
	return Candidate{
		Manager: manager, Datasource: datasource, Name: name,
		CurrentVersion: version, CurrentValue: string(data[span.start:span.end]),
		File: rel, Line: lineNumber(data, span.start), Start: span.start, End: span.end,
	}
}

func splitConstraint(value string) (prefix, suffix, version string) {
	match := constraintToken.FindStringSubmatchIndex(strings.TrimSpace(value))
	if match == nil {
		return "", "", value
	}
	trimmed := strings.TrimSpace(value)
	return trimmed[:match[2]], trimmed[match[3]:], trimmed[match[2]:match[3]]
}

func lineNumber(data []byte, offset int) int {
	return 1 + strings.Count(string(data[:offset]), "\n")
}

type byteRange struct{ start, end int }

func lineRanges(data []byte) []byteRange {
	var result []byteRange
	start := 0
	for index, value := range data {
		if value == '\n' {
			result = append(result, byteRange{start: start, end: index})
			start = index + 1
		}
	}
	if start < len(data) {
		result = append(result, byteRange{start: start, end: len(data)})
	}
	return result
}

func deduplicate(candidates []Candidate) []Candidate {
	seen := map[string]bool{}
	result := make([]Candidate, 0, len(candidates))
	for _, entry := range candidates {
		key := fmt.Sprintf("%s:%d:%d", entry.File, entry.Start, entry.End)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, entry)
	}
	return result
}
