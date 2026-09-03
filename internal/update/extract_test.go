package update

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/openhoo/hooneedsupdates/internal/config"
)

func TestExtractorFindsSupportedManagers(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.test/demo\n\ngo 1.25\n\nrequire example.test/direct v1.2.3\n")
	writeFixture(t, root, "Cargo.toml", "[package]\nname = \"demo\"\nversion = \"9.9.9\"\n\n[dependencies]\nserde = \"1.0.0\"\ntokio = { version = \"1.2.0\", features = [\"rt\"] }\ninternal = { path = \"crates/internal\", version = \"0.1.0\" }\ncomplex = \">=1, <2\"\n")
	writeFixture(t, root, "package.json", `{"name":"demo","version":"7.7.7","dependencies":{"react":"^18.2.0","local":"workspace:*"},"scripts":{"fake":"1.0.0"}}`)
	writeFixture(t, root, "demo.csproj", `<Project><ItemGroup><PackageReference Include="FluentAssertions" Version="6.12.0" /><PackageReference Include="xunit"><Version>2.8.0</Version></PackageReference><PackageReference Include="range" Version="[1.0,2.0)" /></ItemGroup></Project>`)
	writeFixture(t, root, ".github/workflows/ci.yml", "steps:\n  - uses: actions/checkout@0123456789012345678901234567890123456789 # v6\n  - uses: openhoo/hoolicy/actions/check@abcdefabcdefabcdefabcdefabcdefabcdefabcd # v0.2.3\n    with:\n      version: 0.2.3\n  - uses: ./local\n")
	writeFixture(t, root, "Dockerfile", "FROM golang:1.25-alpine AS build\nFROM scratch\n")
	cfg := config.Default()
	candidates, err := (Extractor{Root: root, Config: cfg}).Extract()
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(candidates))
	for _, entry := range candidates {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.File)))
		if err != nil {
			t.Fatal(err)
		}
		if string(data[entry.Start:entry.End]) != entry.CurrentValue {
			t.Fatalf("invalid range for %#v", entry)
		}
		got = append(got, string(entry.Manager)+":"+entry.Name+"@"+entry.CurrentVersion)
	}
	sort.Strings(got)
	want := []string{
		"cargo:serde@1.0.0",
		"cargo:tokio@1.2.0",
		"custom:openhoo/hoolicy@0.2.3",
		"docker:golang@1.25-alpine",
		"github-actions:actions/checkout@v6",
		"github-actions:openhoo/hoolicy@v0.2.3",
		"gomod:example.test/direct@v1.2.3",
		"npm:react@18.2.0",
		"nuget:FluentAssertions@6.12.0",
		"nuget:xunit@2.8.0",
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("got %d candidates %v, want %d %v", len(got), got, len(want), want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestExtractGoMod(t *testing.T) {
	data := []byte("module example.test/demo\n\ngo 1.25\n\nrequire example.test/direct v1.2.3\n")
	entries, err := extractGoMod("go.mod", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %#v", entries)
	}
}

func TestExtractorCustomManagerAndSymlinkBoundary(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.yml")
	if err := os.WriteFile(outside, []byte("HOOVERSION_VERSION: 9.9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.yml")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, ".github/workflows/ci.yml", "env:\n  HOOVERSION_VERSION: 1.0.3\n")
	writeFixture(t, root, "tests/fixtures/package.json", `{"dependencies":{"must-not-appear":"1.0.0"}}`)
	cfg := config.Default()
	cfg.CustomManagers = []config.CustomManager{{
		Name: "hooversion", Datasource: "github-releases", DependencyName: "openhoo/hooversion",
		FilePatterns: []string{`^\.github/workflows/.*\.ya?ml$`},
		MatchStrings: []string{`HOOVERSION_VERSION:\s*(?P<currentValue>\S+)`},
	}}
	candidates, err := (Extractor{Root: root, Config: cfg}).Extract()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].CurrentVersion != "1.0.3" {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
}

func writeFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
func TestExtractCargoConstraintsAndRenamedDependencies(t *testing.T) {
	data := []byte(`[dependencies]
local = { package = "actual", version = "=1.2.3" }
other = { version = "~1.2.3", package = "other-crate" }
prerelease = { package = "pre-crate", version = "=1.2.3-beta.1+build.7" }
commented = { version = "1.2.3" } # package = "not-the-crate"
string = { version = "1.2.3", features = ['fake, package = "not-the-crate"'] }
`)
	entries := extractCargo("Cargo.toml", data)
	want := []struct {
		name, value, version, prefix, suffix string
	}{
		{"actual", "=1.2.3", "1.2.3", "=", ""},
		{"other-crate", "~1.2.3", "1.2.3", "~", ""},
		{"pre-crate", "=1.2.3-beta.1+build.7", "1.2.3-beta.1+build.7", "=", ""},
		{"commented", "1.2.3", "1.2.3", "", ""},
		{"string", "1.2.3", "1.2.3", "", ""},
	}
	if len(entries) != len(want) {
		t.Fatalf("got %#v", entries)
	}
	for index, expected := range want {
		entry := entries[index]
		if entry.Name != expected.name || entry.CurrentValue != expected.value ||
			entry.CurrentVersion != expected.version || entry.Prefix != expected.prefix || entry.Suffix != expected.suffix {
			t.Errorf("entry[%d]=%+v, want name=%q value=%q version=%q prefix=%q suffix=%q",
				index, entry, expected.name, expected.value, expected.version, expected.prefix, expected.suffix)
		}
	}
}

func TestExtractNPMUsesSupportedObjectOffsetsOnly(t *testing.T) {
	data := []byte(`{"dependencies":{"same":"^1.2.3"},"config":{"same":"^1.2.3"},"devDependencies":{"same":"~1.2.3"}}`)
	entries, err := extractNPM("package.json", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %#v", entries)
	}
	for _, entry := range entries {
		if string(data[entry.Start:entry.End]) != entry.CurrentValue || entry.Name != "same" {
			t.Fatalf("invalid entry=%+v", entry)
		}
	}
}

func TestExtractNPMRejectsDuplicateKeys(t *testing.T) {
	tests := []string{
		`{"dependencies":{"demo":"1.0.0","demo":"2.0.0"}}`,
		`{"dependencies":{"demo":"1.0.0"},"dependencies":{"demo":"2.0.0"}}`,
	}
	for _, data := range tests {
		t.Run(data, func(t *testing.T) {
			if _, err := extractNPM("package.json", []byte(data)); err == nil ||
				!strings.Contains(err.Error(), "duplicate package.json") {
				t.Fatalf("extractNPM error=%v, want duplicate-key error", err)
			}
		})
	}
}

func TestExtractNuGetXMLTokensPreserveActiveSpans(t *testing.T) {
	data := []byte(`<Project><ItemGroup><PackageReference Version='1.2.3' Include='Demo' /><PackageVersion Update="Other"><Version>2.0.0</Version></PackageVersion><!-- <PackageReference Include="Commented" Version="9.9.9" /> --></ItemGroup></Project>`)
	entries, err := extractNuGet("Directory.Packages.props", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %#v", entries)
	}
	if entries[0].Name != "Demo" || entries[0].CurrentValue != "1.2.3" || string(data[entries[0].Start:entries[0].End]) != "1.2.3" {
		t.Fatalf("first entry=%+v", entries[0])
	}
	if entries[1].Name != "Other" || entries[1].CurrentValue != "2.0.0" || string(data[entries[1].Start:entries[1].End]) != "2.0.0" {
		t.Fatalf("second entry=%+v", entries[1])
	}
}

func TestExtractNuGetXMLMetadataNamesAreCaseInsensitive(t *testing.T) {
	data := []byte(`<Project><ItemGroup><packagereference include="Demo"><version>1.2.3</version></packagereference><PackageReference Include="Other" version="2.0.0" /></ItemGroup></Project>`)
	entries, err := extractNuGet("Directory.Packages.props", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %#v", entries)
	}
	for _, test := range []struct {
		name, version string
	}{
		{name: "Demo", version: "1.2.3"},
		{name: "Other", version: "2.0.0"},
	} {
		var found *Candidate
		for index := range entries {
			if entries[index].Name == test.name {
				found = &entries[index]
				break
			}
		}
		if found == nil || found.CurrentVersion != test.version ||
			string(data[found.Start:found.End]) != test.version {
			t.Fatalf("entry for %s = %+v", test.name, found)
		}
	}
}

func TestExtractNuGetXMLVersionVariantsWithoutCandidates(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "self-closing",
			data: `<Project><ItemGroup><PackageReference Include="empty"><Version/></PackageReference></ItemGroup></Project>`,
		},
		{
			name: "self-closing with whitespace",
			data: `<Project><ItemGroup><PackageReference Include="empty"><Version /></PackageReference></ItemGroup></Project>`,
		},
		{
			name: "whitespace-only content",
			data: "<Project><ItemGroup><PackageReference Include=\"empty\"><Version> \n\t </Version></PackageReference></ItemGroup></Project>",
		},
		{
			name: "default namespace",
			data: `<Project xmlns="http://schemas.microsoft.com/developer/msbuild/2003"><ItemGroup><PackageReference Include="empty"><Version/></PackageReference></ItemGroup></Project>`,
		},
		{
			name: "prefixed namespace",
			data: `<msb:Project xmlns:msb="http://schemas.microsoft.com/developer/msbuild/2003"><msb:ItemGroup><msb:PackageReference Include="empty"><msb:Version/></msb:PackageReference></msb:ItemGroup></msb:Project>`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entries, err := extractNuGet("Directory.Build.props", []byte(test.data))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("got candidates %#v", entries)
			}
		})
	}
}
