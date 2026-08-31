package update

import (
	"os"
	"path/filepath"
	"sort"
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
