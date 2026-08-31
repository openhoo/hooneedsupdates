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

func TestValidateRejectsUnsafeLockfileTimeout(t *testing.T) {
	for _, value := range []string{"0s", "invalid", "31m"} {
		cfg := Default()
		cfg.LockfileTimeout = value
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "lockfileTimeout") {
			t.Fatalf("LockfileTimeout=%q error=%v", value, err)
		}
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

func TestAutomationDefaultsAndValidation(t *testing.T) {
	cfg := Default()
	if !cfg.Automation.Lockfiles || !cfg.Automation.CloseStale || cfg.Automation.AutoMerge.Enabled || cfg.Automation.MergeMethod != "squash" {
		t.Fatalf("unexpected automation defaults: %+v", cfg.Automation)
	}

	valid := Default()
	valid.Automation.Repositories = []string{"openhoo/hooversion", "openhoo/hoolicy"}
	valid.Automation.AutoMerge.Enabled = true
	valid.Automation.AutoMerge.Managers = []string{"github-actions", "custom"}
	valid.Automation.AutoMerge.Dependencies = []string{`^openhoo/`}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	invalid := []Automation{
		{Repositories: []string{"not-a-repository"}, BranchPrefix: "bot", MergeMethod: "squash", CommitAuthor: "Bot", CommitEmail: "bot@example.com"},
		{Repositories: []string{"openhoo/tool", "OPENHOO/TOOL"}, BranchPrefix: "bot", MergeMethod: "squash", CommitAuthor: "Bot", CommitEmail: "bot@example.com"},
		{Repositories: []string{"openhoo/tool"}, BranchPrefix: "../main", MergeMethod: "squash", CommitAuthor: "Bot", CommitEmail: "bot@example.com"},
		{Repositories: []string{"openhoo/tool"}, BranchPrefix: "bot/.hidden", MergeMethod: "squash", CommitAuthor: "Bot", CommitEmail: "bot@example.com"},
		{Repositories: []string{"openhoo/tool"}, BranchPrefix: "bot", MergeMethod: "fast-forward", AutoMerge: cfg.Automation.AutoMerge, CommitAuthor: "Bot", CommitEmail: "bot@example.com"},
		{Repositories: []string{"openhoo/tool"}, BranchPrefix: "bot", MergeMethod: "squash", Draft: true, AutoMerge: AutoMerge{Enabled: true, UpdateTypes: []string{"patch"}, MaxUpdates: 1, RequireLockfiles: true}, Lockfiles: true, CommitAuthor: "Bot", CommitEmail: "bot@example.com"},
	}
	for _, automation := range invalid {
		cfg := Default()
		cfg.Automation = automation
		if err := cfg.Validate(); err == nil {
			t.Fatalf("invalid automation accepted: %+v", automation)
		}
	}
}

func TestAutomationSelectionRejectsInvalidPolicy(t *testing.T) {
	for _, selection := range []Selection{
		{UpdateTypes: []string{"security"}},
		{Managers: []string{"unknown"}},
		{Dependencies: []string{"["}},
	} {
		cfg := Default()
		cfg.Automation.Selection = selection
		if err := cfg.Validate(); err == nil {
			t.Fatalf("invalid selection accepted: %+v", selection)
		}
	}
}

func TestLoadAutomationPreservesSafeDefaults(t *testing.T) {
	root := t.TempDir()
	contents := "version: 1\nautomation:\n  repositories: [openhoo/hooversion]\n  autoMerge:\n    enabled: true\n"
	if err := os.WriteFile(filepath.Join(root, FileName), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Automation.Lockfiles || !cfg.Automation.CloseStale || cfg.Automation.MergeMethod != "squash" || len(cfg.Automation.AutoMerge.UpdateTypes) != 2 {
		t.Fatalf("defaults were not preserved: %+v", cfg.Automation)
	}
}
