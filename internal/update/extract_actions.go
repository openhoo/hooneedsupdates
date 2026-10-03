package update

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var actionVersion = regexp.MustCompile(`(?m)^\s+version:\s*["']?([^\s"']+)["']?\s*$`)

var actionUse = regexp.MustCompile(`(?m)^\s*(?:-\s*)?uses:\s*["']?([^@\s"']+)@([^#\s"']+)["']?(?:\s*#\s*([^\s]+))?`)

func extractActions(rel string, data []byte) ([]Candidate, error) {
	// YAML structure separates executable references from text in run scripts,
	// descriptions, and unrelated action inputs. Regexes still locate exact bytes.
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, err
	}
	if err := decoder.Decode(&yaml.Node{}); err != io.EOF {
		return nil, fmt.Errorf("workflow must contain exactly one YAML document")
	}
	uses := yamlMatchesByLine(data, actionUse)
	versions := yamlMatchesByLine(data, actionVersion)
	var result []Candidate
	var visit func(*yaml.Node, bool)
	visit = func(node *yaml.Node, step bool) {
		if node.Kind == yaml.MappingNode {
			if step {
				result = append(result, extractActionMapping(rel, data, node, uses, versions)...)
			}
			for i := 0; i+1 < len(node.Content); i += 2 {
				key, value := node.Content[i].Value, node.Content[i+1]
				if key == "steps" && value.Kind == yaml.SequenceNode {
					for _, child := range value.Content {
						visit(child, true)
					}
				} else if key == "jobs" && value.Kind == yaml.MappingNode {
					for j := 1; j < len(value.Content); j += 2 {
						visit(value.Content[j], true)
					}
				} else if key != "with" && key != "env" && key != "inputs" && key != "outputs" {
					visit(value, false)
				}
			}
		} else if node.Kind == yaml.DocumentNode || node.Kind == yaml.SequenceNode {
			for _, child := range node.Content {
				visit(child, false)
			}
		}
	}
	visit(&document, false)
	return result, nil
}

func yamlMatchesByLine(data []byte, pattern *regexp.Regexp) map[int][]int {
	lines := lineRanges(data)
	result := map[int][]int{}
	for _, match := range pattern.FindAllSubmatchIndex(data, -1) {
		line := sort.Search(len(lines), func(i int) bool { return lines[i].start > match[2] })
		result[line] = match
	}
	return result
}

func extractActionMapping(rel string, data []byte, node *yaml.Node, uses, versions map[int][]int) []Candidate {
	use := yamlMappingValue(node, "uses")
	if use == nil || use.Kind != yaml.ScalarNode {
		return nil
	}
	match := uses[use.Line]
	if match == nil || string(data[match[2]:match[3]])+"@"+string(data[match[4]:match[5]]) != use.Value {
		return nil
	}
	entries := extractAction(rel, data, match)
	if len(entries) == 0 || !strings.HasPrefix(entries[0].Name, "openhoo/") {
		return entries
	}
	version := yamlMappingValue(yamlMappingValue(node, "with"), "version")
	if version == nil || version.Kind != yaml.ScalarNode || normalizeVersion(version.Value) == "" {
		return entries
	}
	versionMatch := versions[version.Line]
	if versionMatch == nil {
		return entries
	}
	span := byteRange{versionMatch[2], versionMatch[3]}
	if string(data[span.start:span.end]) == version.Value {
		entries = append(entries, candidate(ManagerCustom, "github-releases", entries[0].Name, version.Value, rel, data, span))
	}
	return entries
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
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
