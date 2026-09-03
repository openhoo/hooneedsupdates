package update

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
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
	actionUse        = regexp.MustCompile(`(?m)^\s*(?:-\s*)?uses:\s*["']?([^@\s"']+)@([^#\s"']+)["']?(?:\s*#\s*([^\s]+))?`)
	actionVersion    = regexp.MustCompile(`(?m)^\s+version:\s*["']?([^\s"']+)["']?\s*$`)
	nextStep         = regexp.MustCompile(`(?m)^\s*-\s+(?:name|uses):`)
	simpleVersion    = regexp.MustCompile(`^[=~^]?v?\d+(?:\.\d+){0,2}(?:[-+][0-9A-Za-z.+-]+)?$`)
	dockerFrom       = regexp.MustCompile(`(?im)^\s*FROM(?:\s+--platform=[^\s]+)?\s+([^\s:@]+(?:/[^\s:@]+)*):([^\s@]+)(?:\s+AS\s+[^\s]+)?\s*$`)
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
		return extractNuGet(rel, data)
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
	active := cargoActiveLines(data, lines)
	section := ""
	for index, span := range lines {
		if !active[index] {
			continue
		}
		line := data[span.start:span.end]
		if parsed, ok := cargoSection(line); ok {
			section = parsed
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

type cargoLexMode uint8

const (
	cargoLexNormal cargoLexMode = iota
	cargoLexMultiBasic
	cargoLexMultiLiteral
)

func cargoActiveLines(data []byte, lines []byteRange) []bool {
	active := make([]bool, len(lines))
	mode := cargoLexNormal
	for index, span := range lines {
		active[index] = mode == cargoLexNormal
		mode = scanCargoLine(data[span.start:span.end], mode)
	}
	return active
}

func scanCargoLine(line []byte, mode cargoLexMode) cargoLexMode {
	for position := 0; position < len(line); {
		switch mode {
		case cargoLexMultiBasic:
			if line[position] == '\\' {
				position += 2
				continue
			}
			if position+3 <= len(line) && string(line[position:position+3]) == `"""` {
				mode = cargoLexNormal
				position += 3
				continue
			}
			position++
		case cargoLexMultiLiteral:
			if position+3 <= len(line) && string(line[position:position+3]) == "'''" {
				mode = cargoLexNormal
				position += 3
				continue
			}
			position++
		default:
			switch line[position] {
			case '#':
				return mode
			case '"', '\'':
				if position+3 <= len(line) && string(line[position:position+3]) == `"""` {
					mode = cargoLexMultiBasic
					position += 3
					continue
				}
				if position+3 <= len(line) && string(line[position:position+3]) == "'''" {
					mode = cargoLexMultiLiteral
					position += 3
					continue
				}
				end, ok := cargoQuotedEnd(line, position)
				if !ok {
					return cargoLexNormal
				}
				position = end
			default:
				position++
			}
		}
	}
	return mode
}

func cargoSection(line []byte) (string, bool) {
	end := cargoCommentStart(line)
	trimmed := strings.TrimSpace(string(line[:end]))
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return "", false
	}
	return strings.Trim(trimmed, "[]"), true
}

func cargoCommentStart(line []byte) int {
	for position := 0; position < len(line); {
		switch line[position] {
		case '#':
			return position
		case '"', '\'':
			end, ok := cargoQuotedEnd(line, position)
			if !ok {
				return len(line)
			}
			position = end
		default:
			position++
		}
	}
	return len(line)
}

func cargoQuotedEnd(line []byte, start int) (int, bool) {
	if start >= len(line) || (line[start] != '"' && line[start] != '\'') {
		return 0, false
	}
	quote := line[start]
	if start+3 <= len(line) && string(line[start:start+3]) == string([]byte{quote, quote, quote}) {
		for position := start + 3; position < len(line); {
			if quote == '"' && line[position] == '\\' {
				position += 2
				continue
			}
			if position+3 <= len(line) && string(line[position:position+3]) == string([]byte{quote, quote, quote}) {
				return position + 3, true
			}
			position++
		}
		return 0, false
	}
	for position := start + 1; position < len(line); {
		if quote == '"' && line[position] == '\\' {
			position += 2
			continue
		}
		if line[position] == quote {
			return position + 1, true
		}
		position++
	}
	return 0, false
}

func cargoSkipSpace(line []byte, position, limit int) int {
	for position < limit && isCargoSpace(line[position]) {
		position++
	}
	return position
}

func cargoKeyChar(value byte) bool {
	return value >= 'A' && value <= 'Z' ||
		value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' ||
		value == '_' || value == '-'
}

func cargoAssignment(line []byte) (name string, valueStart, valueEnd int, table, ok bool) {
	limit := cargoCommentStart(line)
	position := cargoSkipSpace(line, 0, limit)
	nameStart := position
	for position < limit && cargoKeyChar(line[position]) {
		position++
	}
	if position == nameStart {
		return "", 0, 0, false, false
	}
	name = string(line[nameStart:position])
	position = cargoSkipSpace(line, position, limit)
	if position >= limit || line[position] != '=' {
		return "", 0, 0, false, false
	}
	position = cargoSkipSpace(line, position+1, limit)
	if position >= limit {
		return "", 0, 0, false, false
	}
	if line[position] == '{' {
		end, valid := cargoDelimitedEnd(line, position, limit)
		if !valid || cargoSkipSpace(line, end, limit) != limit {
			return "", 0, 0, false, false
		}
		return name, position, end, true, true
	}
	if line[position] != '"' && line[position] != '\'' {
		return "", 0, 0, false, false
	}
	end, valid := cargoQuotedEnd(line, position)
	if !valid || end > limit || cargoSkipSpace(line, end, limit) != limit {
		return "", 0, 0, false, false
	}
	return name, position + 1, end - 1, false, true
}

func cargoDelimitedEnd(line []byte, start, limit int) (int, bool) {
	if start >= limit || (line[start] != '{' && line[start] != '[' && line[start] != '(') {
		return 0, false
	}
	depth := 0
	for position := start; position < limit; {
		switch line[position] {
		case '"', '\'':
			end, ok := cargoQuotedEnd(line, position)
			if !ok || end > limit {
				return 0, false
			}
			position = end
		case '{', '[', '(':
			depth++
			position++
		case '}', ']', ')':
			depth--
			position++
			if depth == 0 {
				return position, true
			}
		default:
			position++
		}
	}
	return 0, false
}

type cargoInlineField struct {
	valueStart int
	valueEnd   int
	quoted     bool
}

func cargoInlineFields(line []byte, start, end int) (map[string]cargoInlineField, bool) {
	fields := map[string]cargoInlineField{}
	limit := end - 1
	position := start + 1
	for {
		position = cargoSkipSpace(line, position, limit)
		for position < limit && line[position] == ',' {
			position = cargoSkipSpace(line, position+1, limit)
		}
		if position >= limit {
			return fields, true
		}
		var key string
		if line[position] == '"' || line[position] == '\'' {
			keyEnd, ok := cargoQuotedEnd(line, position)
			if !ok || keyEnd > limit {
				return nil, false
			}
			key = string(line[position+1 : keyEnd-1])
			position = keyEnd
		} else {
			keyStart := position
			for position < limit && cargoKeyChar(line[position]) {
				position++
			}
			if keyStart == position {
				return nil, false
			}
			key = string(line[keyStart:position])
		}
		position = cargoSkipSpace(line, position, limit)
		if position >= limit || line[position] != '=' {
			return nil, false
		}
		position = cargoSkipSpace(line, position+1, limit)
		if position >= limit {
			return nil, false
		}
		valueStart := position
		quoted := line[position] == '"' || line[position] == '\''
		if quoted {
			valueEnd, ok := cargoQuotedEnd(line, position)
			if !ok || valueEnd > limit {
				return nil, false
			}
			position = valueEnd
		} else if line[position] == '{' || line[position] == '[' || line[position] == '(' {
			valueEnd, ok := cargoDelimitedEnd(line, position, limit)
			if !ok {
				return nil, false
			}
			position = valueEnd
		} else {
			for position < limit && line[position] != ',' {
				position++
			}
		}
		valueEnd := position
		for valueEnd > valueStart && isCargoSpace(line[valueEnd-1]) {
			valueEnd--
		}
		if valueEnd == valueStart {
			return nil, false
		}
		fields[key] = cargoInlineField{valueStart: valueStart, valueEnd: valueEnd, quoted: quoted}
		position = cargoSkipSpace(line, position, limit)
		if position < limit && line[position] != ',' {
			return nil, false
		}
	}
}

func cargoStringField(line []byte, field cargoInlineField) (string, bool) {
	if !field.quoted || field.valueEnd-field.valueStart < 2 {
		return "", false
	}
	return string(line[field.valueStart+1 : field.valueEnd-1]), true
}

func extractCargoLine(rel string, data, line []byte, lineSpan byteRange) (Candidate, bool) {
	name, valueStart, valueEnd, table, ok := cargoAssignment(line)
	if !ok {
		return Candidate{}, false
	}
	if table {
		fields, valid := cargoInlineFields(line, valueStart, valueEnd)
		if !valid || fields == nil || fields["path"].valueEnd != 0 {
			return Candidate{}, false
		}
		versionField, found := fields["version"]
		if !found {
			return Candidate{}, false
		}
		versionValue, valid := cargoStringField(line, versionField)
		if !valid {
			return Candidate{}, false
		}
		valueStart, valueEnd = versionField.valueStart+1, versionField.valueEnd-1
		value := versionValue
		prefix, suffix, version := splitCargoConstraint(value)
		if !simpleVersion.MatchString(value) || normalizeVersion(version) == "" {
			return Candidate{}, false
		}
		if packageField, found := fields["package"]; found {
			if packageName, valid := cargoStringField(line, packageField); valid {
				name = packageName
			}
		}
		span := byteRange{lineSpan.start + valueStart, lineSpan.start + valueEnd}
		entry := candidate(ManagerCargo, "crates.io", name, version, rel, data, span)
		entry.CurrentValue, entry.Prefix, entry.Suffix = value, prefix, suffix
		return entry, true
	}
	value := string(line[valueStart:valueEnd])
	prefix, suffix, version := splitCargoConstraint(value)
	if !simpleVersion.MatchString(value) || normalizeVersion(version) == "" {
		return Candidate{}, false
	}
	span := byteRange{lineSpan.start + valueStart, lineSpan.start + valueEnd}
	entry := candidate(ManagerCargo, "crates.io", name, version, rel, data, span)
	entry.CurrentValue, entry.Prefix, entry.Suffix = value, prefix, suffix
	return entry, true
}

func isCargoSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func splitCargoConstraint(value string) (prefix, suffix, version string) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) > 0 && strings.ContainsRune("=~^", rune(trimmed[0])) {
		return trimmed[:1], "", strings.TrimSpace(trimmed[1:])
	}
	return "", "", trimmed
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
	pos := skipJSONSpace(data, 0)
	if pos >= len(data) || data[pos] != '{' {
		return nil, nil
	}
	supported := map[string]bool{
		"dependencies": true, "devDependencies": true,
		"optionalDependencies": true, "peerDependencies": true,
	}
	seenSections := make(map[string]struct{})
	var result []Candidate
	pos++
	for {
		pos = skipJSONSpace(data, pos)
		if pos >= len(data) {
			return nil, fmt.Errorf("invalid package.json object")
		}
		if data[pos] == '}' {
			break
		}
		name, _, _, next, err := jsonStringAt(data, pos)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seenSections[name]; duplicate {
			return nil, fmt.Errorf("duplicate package.json member %q", name)
		}
		seenSections[name] = struct{}{}
		pos = skipJSONSpace(data, next)
		if pos >= len(data) || data[pos] != ':' {
			return nil, fmt.Errorf("invalid package.json member")
		}
		pos = skipJSONSpace(data, pos+1)
		if supported[name] && pos < len(data) && data[pos] == '{' {
			entries, end, err := extractNPMDependencyObject(rel, data, pos)
			if err != nil {
				return nil, err
			}
			result = append(result, entries...)
			pos = end
		} else {
			pos, err = skipJSONValue(data, pos)
			if err != nil {
				return nil, err
			}
		}
		pos = skipJSONSpace(data, pos)
		if pos < len(data) && data[pos] == ',' {
			pos++
			continue
		}
		if pos < len(data) && data[pos] == '}' {
			break
		}
		return nil, fmt.Errorf("invalid package.json object separator")
	}
	return result, nil
}

func extractNPMDependencyObject(rel string, data []byte, pos int) ([]Candidate, int, error) {
	var result []Candidate
	seenDependencies := make(map[string]struct{})
	pos++
	for {
		pos = skipJSONSpace(data, pos)
		if pos >= len(data) {
			return nil, 0, fmt.Errorf("invalid package.json dependency object")
		}
		if data[pos] == '}' {
			return result, pos + 1, nil
		}
		name, _, _, next, err := jsonStringAt(data, pos)
		if err != nil {
			return nil, 0, err
		}
		if _, duplicate := seenDependencies[name]; duplicate {
			return nil, 0, fmt.Errorf("duplicate package.json dependency %q", name)
		}
		seenDependencies[name] = struct{}{}
		pos = skipJSONSpace(data, next)
		if pos >= len(data) || data[pos] != ':' {
			return nil, 0, fmt.Errorf("invalid package.json dependency member")
		}
		pos = skipJSONSpace(data, pos+1)
		if pos >= len(data) {
			return nil, 0, fmt.Errorf("invalid package.json dependency value")
		}
		if data[pos] == '"' {
			value, start, end, next, err := jsonStringAt(data, pos)
			if err != nil {
				return nil, 0, err
			}
			prefix, suffix, version := splitConstraint(value)
			if supportedNPMConstraint(prefix, suffix, version) {
				entry := candidate(ManagerNPM, "npm", name, version, rel, data, byteRange{start, end})
				entry.CurrentValue, entry.Prefix, entry.Suffix = string(data[start:end]), prefix, suffix
				result = append(result, entry)
			}
			pos = next
		} else {
			pos, err = skipJSONValue(data, pos)
			if err != nil {
				return nil, 0, err
			}
		}
		pos = skipJSONSpace(data, pos)
		if pos < len(data) && data[pos] == ',' {
			pos++
			continue
		}
		if pos < len(data) && data[pos] == '}' {
			return result, pos + 1, nil
		}
		return nil, 0, fmt.Errorf("invalid package.json dependency separator")
	}
}

func skipJSONSpace(data []byte, pos int) int {
	for pos < len(data) {
		switch data[pos] {
		case ' ', '\t', '\r', '\n':
			pos++
		default:
			return pos
		}
	}
	return pos
}

func jsonStringAt(data []byte, pos int) (string, int, int, int, error) {
	if pos >= len(data) || data[pos] != '"' {
		return "", 0, 0, 0, fmt.Errorf("invalid JSON string")
	}
	for index := pos + 1; index < len(data); index++ {
		switch data[index] {
		case '\\':
			index++
		case '"':
			rawEnd := index
			var value string
			if err := json.Unmarshal(data[pos:index+1], &value); err != nil {
				return "", 0, 0, 0, err
			}
			return value, pos + 1, rawEnd, index + 1, nil
		}
	}
	return "", 0, 0, 0, fmt.Errorf("unterminated JSON string")
}

func skipJSONValue(data []byte, pos int) (int, error) {
	if pos >= len(data) {
		return 0, fmt.Errorf("missing JSON value")
	}
	if data[pos] == '"' {
		_, _, _, next, err := jsonStringAt(data, pos)
		return next, err
	}
	if data[pos] == '{' || data[pos] == '[' {
		open := data[pos]
		close := byte('}')
		if open == '[' {
			close = ']'
		}
		depth := 0
		for index := pos; index < len(data); index++ {
			if data[index] == '"' {
				_, _, _, next, err := jsonStringAt(data, index)
				if err != nil {
					return 0, err
				}
				index = next - 1
				continue
			}
			if data[index] == open {
				depth++
			} else if data[index] == close {
				depth--
				if depth == 0 {
					return index + 1, nil
				}
			}
		}
		return 0, fmt.Errorf("unterminated JSON value")
	}
	for index := pos; index < len(data); index++ {
		switch data[index] {
		case ' ', '\t', '\r', '\n', ',', '}', ']':
			return index, nil
		}
	}
	return len(data), nil
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

type nugetXMLAttribute struct {
	value string
	span  byteRange
}

type nugetXMLFrame struct {
	local        string
	active       bool
	name         string
	version      string
	versionSpan  byteRange
	hasVersion   bool
	childVersion bool
	contentStart int
	text         string
}

func extractNuGet(rel string, data []byte) ([]Candidate, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var stack []nugetXMLFrame
	var result []Candidate
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			end := int(decoder.InputOffset())
			start := bytes.LastIndexByte(data[:end], '<')
			attributes, ok := parseNuGetXMLAttributes(data, start, end)
			if !ok {
				return nil, fmt.Errorf("invalid XML start element %s", value.Name.Local)
			}
			frame := nugetXMLFrame{local: value.Name.Local}
			if strings.EqualFold(value.Name.Local, "PackageReference") || strings.EqualFold(value.Name.Local, "PackageVersion") {
				frame.active = true
				for _, attr := range value.Attr {
					switch {
					case strings.EqualFold(attr.Name.Local, "Include"):
						frame.name = attr.Value
					case strings.EqualFold(attr.Name.Local, "Update"):
						if frame.name == "" {
							frame.name = attr.Value
						}
					}
				}
				var raw nugetXMLAttribute
				var found bool
				for name, attribute := range attributes {
					if strings.EqualFold(name, "Version") {
						raw, found = attribute, true
						break
					}
				}
				if found && raw.value == rawXMLValue(value.Attr, "Version") {
					frame.version = raw.value
					frame.versionSpan = raw.span
					frame.hasVersion = true
				}
			} else if strings.EqualFold(value.Name.Local, "Version") && len(stack) > 0 && stack[len(stack)-1].active &&
				!stack[len(stack)-1].hasVersion {
				frame.childVersion = true
				frame.contentStart = end
			}
			stack = append(stack, frame)
		case xml.CharData:
			if len(stack) > 0 && stack[len(stack)-1].childVersion {
				stack[len(stack)-1].text += string(value)
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("unexpected XML end element %s", value.Name.Local)
			}
			index := len(stack) - 1
			frame := &stack[index]
			end := int(decoder.InputOffset())
			if frame.childVersion {
				closeStart := bytes.LastIndexByte(data[:end], '<')
				if closeStart >= frame.contentStart {
					rawBytes := data[frame.contentStart:closeStart]
					raw := strings.TrimSpace(string(rawBytes))
					decoded := strings.TrimSpace(frame.text)
					if raw != "" && raw == decoded {
						prefix, suffix, version := splitConstraint(raw)
						if exactConstraint(prefix, suffix, version) && index > 0 {
							leading := len(rawBytes) - len(strings.TrimLeft(string(rawBytes), " \t\r\n"))
							parent := &stack[index-1]
							parent.version, parent.versionSpan = version, byteRange{frame.contentStart + leading, frame.contentStart + leading + len(raw)}
							parent.hasVersion = true
						}
					}
				}
			}
			if frame.active && frame.name != "" && frame.hasVersion {
				prefix, suffix, version := splitConstraint(frame.version)
				if exactConstraint(prefix, suffix, version) {
					entry := candidate(ManagerNuGet, "nuget", frame.name, version, rel, data, frame.versionSpan)
					entry.CurrentValue, entry.Prefix, entry.Suffix = frame.version, prefix, suffix
					result = append(result, entry)
				}
			}
			stack = stack[:index]
		}
	}
	return result, nil
}

func rawXMLValue(attrs []xml.Attr, name string) string {
	for _, attr := range attrs {
		if strings.EqualFold(attr.Name.Local, name) {
			return attr.Value
		}
	}
	return ""
}

func parseNuGetXMLAttributes(data []byte, start, end int) (map[string]nugetXMLAttribute, bool) {
	if start < 0 || end > len(data) || start >= end {
		return nil, false
	}
	pos := start + 1
	for pos < end && !isXMLSpace(data[pos]) && data[pos] != '>' && data[pos] != '/' {
		pos++
	}
	result := map[string]nugetXMLAttribute{}
	for pos < end {
		for pos < end && (isXMLSpace(data[pos]) || data[pos] == '/') {
			pos++
		}
		if pos >= end || data[pos] == '>' {
			return result, true
		}
		nameStart := pos
		for pos < end && !isXMLSpace(data[pos]) && data[pos] != '=' && data[pos] != '>' {
			pos++
		}
		name := string(data[nameStart:pos])
		for pos < end && isXMLSpace(data[pos]) {
			pos++
		}
		if pos >= end || data[pos] != '=' {
			return nil, false
		}
		pos = skipXMLSpace(data, pos+1)
		if pos >= end || (data[pos] != '"' && data[pos] != '\'') {
			return nil, false
		}
		quote := data[pos]
		valueStart := pos + 1
		pos = valueStart
		for pos < end && data[pos] != quote {
			pos++
		}
		if pos >= end {
			return nil, false
		}
		result[name] = nugetXMLAttribute{value: string(data[valueStart:pos]), span: byteRange{valueStart, pos}}
		pos++
	}
	return nil, false
}

func isXMLSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func skipXMLSpace(data []byte, pos int) int {
	for pos < len(data) && isXMLSpace(data[pos]) {
		pos++
	}
	return pos
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
