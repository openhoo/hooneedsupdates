package update

import (
	"encoding/json"
	"fmt"
)

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
