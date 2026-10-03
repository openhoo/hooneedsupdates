package update

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func savedFixture(t *testing.T) (string, Plan) {
	t.Helper()
	root := t.TempDir()
	before := []byte("require example.test/x v1.0.0\n")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), before, 0644); err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(before, []byte("v1.0.0"))
	report := Report{SchemaVersion: 2, GeneratedAt: time.Now().UTC(), Root: filepath.ToSlash(root), Summary: Summary{Detected: 1, Outdated: 1}, Updates: []Update{{Candidate: Candidate{Manager: ManagerGoMod, Datasource: "go", Name: "example.test/x", CurrentVersion: "v1.0.0", CurrentValue: "v1.0.0", File: "go.mod", Start: start, End: start + 6}, Status: "outdated", LatestVersion: "v1.1.0"}}}
	report.PlanDigest = planDigest(report.Updates)
	files, err := Apply(root, report, false)
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, AppliedFile{Path: "go.sum", Created: true, Kind: "lockfile", After: []byte("reviewed lock output\n")})
	plan, err := CreatePlan(root, report, files, true)
	if err != nil {
		t.Fatal(err)
	}
	return root, plan
}
func TestSavedPlanRoundTripOfflineAndExactOutput(t *testing.T) {
	root, plan := savedFixture(t)
	path := filepath.Join(t.TempDir(), "review.json")
	if err := SavePlan(path, plan); err != nil {
		t.Fatal(err)
	}
	if err := SavePlan(path, plan); err == nil {
		t.Fatal("overwrote existing review")
	}
	loaded, err := LoadPlan(path)
	if err != nil {
		t.Fatal(err)
	}
	files, err := ApplyPlan(root, loaded, false)
	if err != nil || len(files) != 2 {
		t.Fatalf("preview: %v %v", files, err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.sum")); !os.IsNotExist(err) {
		t.Fatal("preview created lockfile")
	}
	if _, err := ApplyPlan(root, loaded, true); err != nil {
		t.Fatal(err)
	}
	for _, saved := range loaded.Files {
		data, err := os.ReadFile(filepath.Join(root, saved.Path))
		if err != nil || !bytes.Equal(data, saved.After) {
			t.Fatalf("output %s differs: %v", saved.Path, err)
		}
	}
	if _, err := ApplyPlan(root, loaded, true); err == nil {
		t.Fatal("replayed stale plan")
	}
}
func TestSavedPlanRejectsSourceDriftBeforeAnyWrite(t *testing.T) {
	for _, kind := range []string{"unrelated-content", "appeared-lockfile", "different-root", "symlink", "corruption"} {
		t.Run(kind, func(t *testing.T) {
			root, plan := savedFixture(t)
			switch kind {
			case "unrelated-content":
				os.WriteFile(filepath.Join(root, "go.mod"), []byte("// changed\nrequire example.test/x v1.0.0\n"), 0644)
			case "appeared-lockfile":
				os.WriteFile(filepath.Join(root, "go.sum"), []byte("human output"), 0644)
			case "different-root":
				root = t.TempDir()
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				os.WriteFile(target, []byte("reviewed lock output"), 0644)
				if err := os.Symlink(target, filepath.Join(root, "go.sum")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "corruption":
				plan.Files[0].After = []byte("different output")
			}
			if _, err := ApplyPlan(root, plan, true); err == nil {
				t.Fatal("unsafe saved plan accepted")
			}
			if kind != "appeared-lockfile" && kind != "symlink" {
				if _, err := os.Stat(filepath.Join(root, "go.sum")); !os.IsNotExist(err) {
					t.Fatal("failed apply created lockfile")
				}
			}
		})
	}
}
func TestSavedPlanStrictDecodeAndIncompleteInventory(t *testing.T) {
	_, plan := savedFixture(t)
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{string(data) + "{}", strings.Replace(string(data), `"schemaVersion":1`, `"schemaVersion":99`, 1), strings.Replace(string(data), `"schemaVersion":1`, `"unknown":1,"schemaVersion":1`, 1)} {
		path := filepath.Join(t.TempDir(), "plan.json")
		os.WriteFile(path, []byte(input), 0600)
		if _, err := LoadPlan(path); err == nil {
			t.Fatal("invalid plan accepted")
		}
	}
	for _, status := range []string{"unresolved", "blocked", "unsupported"} {
		if err := RequireComplete(Report{Updates: []Update{{Status: status}}}); err == nil {
			t.Fatalf("%s accepted", status)
		}
	}
}
func TestReviewDiffAppliesExactBytes(t *testing.T) {
	for _, test := range []struct {
		name, before, after string
		created             bool
	}{
		{"changed", "one\ntwo\nthree\nfour\nfive\n", "one\nTWO\nthree\nfour\nfive\n", false},
		{"new", "", "new\n", true}, {"empty", "old\n", "", false},
		{"no-newline", "old", "new", false}, {"insert-final-newline", "old", "old\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "input.txt")
			if !test.created {
				os.WriteFile(path, []byte(test.before), 0644)
			}
			var diff bytes.Buffer
			if err := WriteDiff(&diff, []AppliedFile{{Path: "input.txt", Before: []byte(test.before), After: []byte(test.after), Created: test.created}}); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("git", "-c", "core.autocrlf=false", "apply", "--unsafe-paths", "-")
			command.Dir = root
			command.Stdin = &diff
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("diff rejected: %v %s", err, output)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != test.after {
				t.Fatalf("different bytes %q: %v", data, err)
			}
		})
	}
}
