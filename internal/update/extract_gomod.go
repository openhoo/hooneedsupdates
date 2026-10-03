package update

import (
	"golang.org/x/mod/modfile"
	"regexp"
)

var goRequireLine = regexp.MustCompile(`(?m)^\s*(?:require\s+)?([^\s]+)\s+(v[^\s]+)(?:\s+//\s*indirect)?\s*$`)

func extractGoMod(rel string, data []byte) ([]Candidate, error) {
	parsed, err := modfile.Parse(rel, data, nil)
	if err != nil {
		return nil, err
	}
	required := make(map[string]string, len(parsed.Require))
	for _, requirement := range parsed.Require {
		if !requirement.Indirect {
			required[requirement.Mod.Path] = requirement.Mod.Version
		}
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
