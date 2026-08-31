package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsUnknownFields(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("version: 1\nmanagers: [gomod]\nconcurrency: 1\nunknown: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(root, "")
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRequiresSchemaVersion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("managers: [gomod]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(root, "")
	if err == nil || !strings.Contains(err.Error(), "version is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRequiresReasonAndNamedCapture(t *testing.T) {
	cfg := Default()
	cfg.Ignore = []IgnoreRule{{Dependency: "x"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("missing ignore reason accepted")
	}
	cfg = Default()
	cfg.CustomManagers = []CustomManager{{Name: "x", Datasource: "github-releases", DependencyName: "o/r", FilePatterns: []string{".*"}, MatchStrings: []string{"version: (.*)"}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("missing named capture accepted")
	}
}

func TestLoadResolvesExplicitPathAgainstRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "custom.yml"), []byte("version: 1\nmanagers: [gomod]\nconcurrency: 1\nrequestTimeout: 1s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, path, err := Load(root, "custom.yml")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(root, "custom.yml") {
		t.Fatalf("path=%q", path)
	}
}
