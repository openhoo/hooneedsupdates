package update

import (
	"strings"
)

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
		if !valid || fields == nil || fields["path"].valueEnd != 0 || fields["git"].valueEnd != 0 || fields["registry"].valueEnd != 0 {
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
