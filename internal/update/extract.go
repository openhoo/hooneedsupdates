package update

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/openhoo/hooneedsupdates/internal/config"
	"golang.org/x/mod/modfile"
)

const maxManifestSize = 5 << 20

var (
	goRequireLine    = regexp.MustCompile(`(?m)^\s*(?:require\s+)?([^\s]+)\s+(v[^\s]+)(?:\s+//\s*indirect)?\s*$`)
	cargoSimple      = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_-]+)\s*=\s*"([^"]+)"\s*(?:#.*)?$`)
	cargoTable       = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_-]+)\s*=\s*\{[^}\n]*\bversion\s*=\s*"([^"]+)"[^}\n]*\}\s*(?:#.*)?$`)
	cargoPath        = regexp.MustCompile(`\bpath\s*=`)
	npmPair          = regexp.MustCompile(`"([^"]+)"\s*:\s*"([^"]+)"`)
	nugetInline      = regexp.MustCompile(`(?i)<Package(?:Reference|Version)\b[^>]*(?:Include|Update)\s*=\s*"([^"]+)"[^>]*\bVersion\s*=\s*"([^"]+)"[^>]*/?>`)
	nugetBlock       = regexp.MustCompile(`(?is)<PackageReference\b[^>]*(?:Include|Update)\s*=\s*"([^"]+)"[^>]*>\s*<Version>\s*([^<\s]+)\s*</Version>\s*</PackageReference>`)
	actionUse        = regexp.MustCompile(`(?m)^\s*(?:-\s*)?uses:\s*["']?([^@\s"']+)@([^#\s"']+)["']?(?:\s*#\s*([^\s]+))?`)
	actionVersion    = regexp.MustCompile(`(?m)^\s+version:\s*["']?([^\s"']+)["']?\s*$`)
	nextStep         = regexp.MustCompile(`(?m)^\s*-\s+(?:name|uses):`)
	dockerFrom       = regexp.MustCompile(`(?im)^\s*FROM(?:\s+--platform=[^\s]+)?\s+([^\s:@]+(?:/[^\s:@]+)*):([^\s@]+)(?:\s+AS\s+[^\s]+)?\s*$`)
	simpleVersion    = regexp.MustCompile(`^[=~^]?v?\d+(?:\.\d+){0,2}(?:[-+][0-9A-Za-z.-]+)?$`)
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
	root, err := filepath.Abs(e.Root)
	if err != nil {
		return nil, err
	}
	var candidates []Candidate
	err = filepath.WalkDir(root, e.walkEntry(root, &candidates))
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
			if rel != "." && excludedDirectory(entry.Name()) {
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
	if info.Size() > maxManifestSize {
		return nil, nil
	}
	manager := managerFor(rel, e.Config)
	custom := matchingCustomManagers(rel, e.Config.CustomManagers)
	if manager == "" && len(custom) == 0 {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
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
		return extractNuGet(rel, data), nil
	case ManagerGitHubActions:
		return extractActions(rel, data), nil
	case ManagerDocker:
		return extractDocker(rel, data), nil
	default:
		return nil, fmt.Errorf("unsupported manager %q", manager)
	}
}

func extractGoMod(rel string, data []byte) ([]Candidate, error) {
	parsed, err := modfile.Parse(rel, data, nil)
	if err != nil {
		return nil, err
	}
	required := make(map[string]string, len(parsed.Require))
	for _, requirement := range parsed.Require {
		required[requirement.Mod.Path] = requirement.Mod.Version
	}
	var result []Candidate
	for _, match := range goRequireLine.FindAllSubmatchIndex(data, -1) {
		name := string(data[match[2]:match[3]])
		value := string(data[match[4]:match[5]])
		if required[name] != value {
			continue
		}
		result = append(result, candidate(ManagerGoMod, "go", name, value, rel, data, byteRange{match[4], match[5]}))
	}
	return result, nil
}

func extractCargo(rel string, data []byte) []Candidate {
	var result []Candidate
	lines := lineRanges(data)
	section := ""
	for _, span := range lines {
		line := data[span.start:span.end]
		trimmed := strings.TrimSpace(string(line))
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.Trim(trimmed, "[]")
			continue
		}
		if !cargoDependencySection(section) {
			continue
		}
		if entry, ok := extractCargoLine(rel, data, line, span); ok {
			result = append(result, entry)
		}
	}
	return result
}

func extractCargoLine(rel string, data, line []byte, lineSpan byteRange) (Candidate, bool) {
	for _, pattern := range []*regexp.Regexp{cargoSimple, cargoTable} {
		match := pattern.FindSubmatchIndex(line)
		if match == nil {
			continue
		}
		if pattern == cargoTable && cargoPath.Match(line) {
			return Candidate{}, false
		}
		span := byteRange{lineSpan.start + match[4], lineSpan.start + match[5]}
		value := string(data[span.start:span.end])
		if !simpleVersion.MatchString(value) {
			return Candidate{}, false
		}
		name := string(line[match[2]:match[3]])
		return candidate(ManagerCargo, "crates.io", name, value, rel, data, span), true
	}
	return Candidate{}, false
}

func cargoDependencySection(section string) bool {
	if cargoSections[section] {
		return true
	}
	for _, suffix := range []string{".dependencies", ".dev-dependencies", ".build-dependencies"} {
		if strings.HasSuffix(section, suffix) {
			return true
		}
	}
	return false
}

func extractNPM(rel string, data []byte) ([]Candidate, error) {
	var manifest struct {
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	allowed := map[string]string{}
	for _, dependencies := range []map[string]string{manifest.Dependencies, manifest.DevDependencies, manifest.OptionalDependencies, manifest.PeerDependencies} {
		for name, version := range dependencies {
			allowed[name] = version
		}
	}
	var result []Candidate
	for _, match := range npmPair.FindAllSubmatchIndex(data, -1) {
		name := string(data[match[2]:match[3]])
		value := string(data[match[4]:match[5]])
		if allowed[name] != value {
			continue
		}
		prefix, suffix, version := splitConstraint(value)
		if !supportedNPMConstraint(prefix, suffix, version) {
			continue
		}
		entry := candidate(ManagerNPM, "npm", name, version, rel, data, byteRange{match[4], match[5]})
		entry.CurrentValue, entry.Prefix, entry.Suffix = value, prefix, suffix
		result = append(result, entry)
	}
	return result, nil
}

func supportedNPMConstraint(prefix, suffix, version string) bool {
	if normalizeVersion(version) == "" || suffix != "" {
		return false
	}
	switch prefix {
	case "", "^", "~", "=":
		return true
	default:
		return false
	}
}

func extractNuGet(rel string, data []byte) []Candidate {
	var result []Candidate
	for _, pattern := range []*regexp.Regexp{nugetInline, nugetBlock} {
		for _, match := range pattern.FindAllSubmatchIndex(data, -1) {
			name := string(data[match[2]:match[3]])
			value := string(data[match[4]:match[5]])
			prefix, suffix, version := splitConstraint(value)
			if !exactConstraint(prefix, suffix, version) {
				continue
			}
			entry := candidate(ManagerNuGet, "nuget", name, version, rel, data, byteRange{match[4], match[5]})
			entry.CurrentValue, entry.Prefix, entry.Suffix = value, prefix, suffix
			result = append(result, entry)
		}
	}
	return result
}

func exactConstraint(prefix, suffix, version string) bool {
	return prefix == "" && suffix == "" && normalizeVersion(version) != ""
}

func extractActions(rel string, data []byte) []Candidate {
	var result []Candidate
	for _, match := range actionUse.FindAllSubmatchIndex(data, -1) {
		result = append(result, extractAction(rel, data, match)...)
	}
	return result
}

func extractAction(rel string, data []byte, match []int) []Candidate {
	name := string(data[match[2]:match[3]])
	if strings.HasPrefix(name, "./") || strings.HasPrefix(name, "docker://") {
		return nil
	}
	parts := strings.Split(name, "/")
	if len(parts) < 2 {
		return nil
	}
	packageName := parts[0] + "/" + parts[1]
	ref := string(data[match[4]:match[5]])
	version := actionDisplayVersion(data, match, ref)
	result := []Candidate{candidate(ManagerGitHubActions, "github-releases", packageName, version, rel, data, byteRange{match[4], match[5]})}
	if versionInput, ok := openHooActionVersion(rel, data, match, packageName); ok {
		result = append(result, versionInput)
	}
	return result
}

func actionDisplayVersion(data []byte, match []int, fallback string) string {
	if len(match) < 8 || match[6] < 0 {
		return fallback
	}
	comment := string(data[match[6]:match[7]])
	if normalizeVersion(comment) != "" {
		return comment
	}
	return fallback
}

func openHooActionVersion(rel string, data []byte, match []int, packageName string) (Candidate, bool) {
	if !strings.HasPrefix(packageName, "openhoo/") {
		return Candidate{}, false
	}
	blockEnd := len(data)
	if next := nextStep.FindIndex(data[match[1]:]); next != nil {
		blockEnd = match[1] + next[0]
	}
	versionMatch := actionVersion.FindSubmatchIndex(data[match[1]:blockEnd])
	if versionMatch == nil {
		return Candidate{}, false
	}
	span := byteRange{match[1] + versionMatch[2], match[1] + versionMatch[3]}
	value := string(data[span.start:span.end])
	if normalizeVersion(value) == "" {
		return Candidate{}, false
	}
	return candidate(ManagerCustom, "github-releases", packageName, value, rel, data, span), true
}

func extractDocker(rel string, data []byte) []Candidate {
	var result []Candidate
	for _, match := range dockerFrom.FindAllSubmatchIndex(data, -1) {
		name := string(data[match[2]:match[3]])
		tag := string(data[match[4]:match[5]])
		if normalizeVersion(tag) == "" {
			continue
		}
		result = append(result, candidate(ManagerDocker, "docker", name, tag, rel, data, byteRange{match[4], match[5]}))
	}
	return result
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
	match := constrainedVersion.FindStringSubmatchIndex(strings.TrimSpace(value))
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
