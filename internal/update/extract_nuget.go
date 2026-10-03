package update

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

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
