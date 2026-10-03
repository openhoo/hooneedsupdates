// Command schema regenerates the public JSON schemas from the Go wire models.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/openhoo/hooneedsupdates/internal/config"
	"github.com/openhoo/hooneedsupdates/internal/update"
)

type schema map[string]any

func main() {
	output := flag.String("output", "schemas", "schema output directory")
	flag.Parse()
	if err := os.MkdirAll(*output, 0755); err != nil {
		panic(err)
	}
	for name, document := range documents() {
		data, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(*output, name+".schema.json"), append(data, '\n'), 0644); err != nil {
			panic(err)
		}
	}
}
func documents() map[string]schema {
	documents := map[string]schema{
		"config": typeSchema(reflect.TypeOf(config.Config{}), "yaml"),
		"report": typeSchema(reflect.TypeOf(update.Report{}), "json"),
		"plan":   typeSchema(reflect.TypeOf(update.Plan{}), "json"),
	}
	for name, document := range documents {
		document["$schema"] = "https://json-schema.org/draft/2020-12/schema"
		document["title"] = "HooNeedsUpdates " + name
		document["description"] = "Generated from Go wire models. Runtime also validates paths, policy conflicts, version windows, source hashes, and checksums."
	}
	configDoc := documents["config"]
	configDoc["required"] = []string{"version"}
	cfg := configDoc["properties"].(map[string]schema)
	cfg["version"]["const"] = 1
	cfg["concurrency"]["minimum"], cfg["concurrency"]["maximum"] = 1, 32
	managerEnums := []string{"gomod", "cargo", "npm", "nuget", "github-actions", "docker"}
	cfg["managers"]["items"].(schema)["enum"] = managerEnums
	cfg["managers"]["minItems"], cfg["managers"]["uniqueItems"] = 1, true
	cfg["allowedUpdateTypes"]["items"].(schema)["enum"] = []string{"patch", "minor", "major"}
	rules := cfg["packageRules"]["items"].(schema)
	rules["required"] = []string{"dependency"}
	ruleProperties := rules["properties"].(map[string]schema)
	ruleProperties["dependency"]["minLength"] = 1
	ruleProperties["channel"]["enum"] = []string{"", "stable", "prerelease"}
	ruleProperties["group"]["pattern"] = `^$|^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$`
	ruleProperties["minimumAge"]["description"] = "Go duration between 0s and 8760h. Missing publication metadata blocks the update."
	report := documents["report"]["properties"].(map[string]schema)
	report["schemaVersion"]["const"] = 2
	report["planDigest"]["pattern"] = `^sha256:[0-9a-f]{64}$`
	entries := report["updates"]["items"].(schema)["properties"].(map[string]schema)
	entries["status"]["enum"] = []string{"current", "outdated", "unresolved", "ignored", "blocked", "unsupported"}
	entries["manager"]["enum"] = append(managerEnums, "custom")
	for _, field := range []string{"currentDigest", "latestDigest"} {
		entries[field]["description"] = "Docker uses sha256:<64 hex>; GitHub Actions latestDigest uses a 40 character commit SHA."
	}
	plan := documents["plan"]["properties"].(map[string]schema)
	plan["schemaVersion"]["const"] = 1
	plan["report"] = schema{"$ref": "report.schema.json"}
	plan["checksum"]["pattern"] = `^sha256:[0-9a-f]{64}$`
	files := plan["files"]["items"].(schema)["properties"].(map[string]schema)
	files["mode"]["maximum"] = 511
	files["kind"]["enum"] = []string{"manifest", "lockfile"}
	files["beforeDigest"]["pattern"] = `^sha256:[0-9a-f]{64}$`
	files["after"]["contentEncoding"] = "base64"
	files["path"]["description"] = "Canonical repository relative path; absolute paths, traversal and symlinks are rejected by apply."
	return documents
}
func typeSchema(t reflect.Type, tag string) schema {
	if t.Kind() == reflect.Pointer {
		return typeSchema(t.Elem(), tag)
	}
	if t == reflect.TypeOf(time.Time{}) {
		return schema{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.Struct:
		properties := map[string]schema{}
		var required []string
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.Anonymous {
				embedded := typeSchema(field.Type, tag)
				for name, definition := range embedded["properties"].(map[string]schema) {
					properties[name] = definition
				}
				if fields, ok := embedded["required"].([]string); ok {
					required = append(required, fields...)
				}
				continue
			}
			raw := field.Tag.Get(tag)
			name, options, _ := strings.Cut(raw, ",")
			if name == "-" || name == "" {
				continue
			}
			properties[name] = typeSchema(field.Type, tag)
			if !strings.Contains(options, "omitempty") {
				required = append(required, name)
			}
		}
		sort.Strings(required)
		result := schema{"type": "object", "additionalProperties": false, "properties": properties}
		if len(required) > 0 {
			result["required"] = required
		}
		return result
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return schema{"type": []string{"string", "null"}}
		}
		return schema{"type": []string{"array", "null"}, "items": typeSchema(t.Elem(), tag)}
	case reflect.String:
		return schema{"type": "string"}
	case reflect.Bool:
		return schema{"type": "boolean"}
	case reflect.Int, reflect.Int32, reflect.Int64, reflect.Uint32:
		return schema{"type": "integer", "minimum": 0}
	default:
		panic(fmt.Sprintf("unsupported schema type %s", t))
	}
}
