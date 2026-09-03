package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyIsPreviewFirstAndUpdatesActionComment(t *testing.T) {
	root := t.TempDir()
	oldDigest := strings.Repeat("a", 40)
	newDigest := strings.Repeat("b", 40)
	content := "steps:\n  - uses: actions/checkout@" + oldDigest + " # v6\n"
	path := filepath.Join(root, "ci.yml")
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	start := strings.Index(content, oldDigest)
	report := Report{Updates: []Update{{
		Candidate:     Candidate{Manager: ManagerGitHubActions, Name: "actions/checkout", CurrentVersion: "v6", CurrentValue: oldDigest, File: "ci.yml", Line: 2, Start: start, End: start + len(oldDigest)},
		LatestVersion: "v7.0.1", LatestDigest: newDigest, Status: "outdated",
	}}}
	preview, err := Apply(root, report, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview) != 1 || string(preview[0].After) != strings.ReplaceAll(strings.ReplaceAll(content, oldDigest, newDigest), "# v6", "# v7.0.1") {
		t.Fatalf("unexpected preview: %q", preview[0].After)
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != content {
		t.Fatal("preview modified file")
	}
	if _, err := Apply(root, report, true); err != nil {
		t.Fatal(err)
	}
	written, _ := os.ReadFile(path)
	if !strings.Contains(string(written), newDigest+" # v7.0.1") {
		t.Fatalf("unexpected written content: %s", written)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed to %o", info.Mode().Perm())
	}
}

func TestApplyFailsClosedWhenSourceChanged(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "go.mod")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Report{Updates: []Update{{Candidate: Candidate{
		Manager: ManagerGoMod, Name: "example.test/x", CurrentVersion: "v1.0.0", CurrentValue: "v1.0.0",
		File: "go.mod", Start: 0, End: len("v1.0.0"),
	}, LatestVersion: "v1.1.0", Status: "outdated"}}}
	if _, err := Apply(root, report, true); err == nil {
		t.Fatal("expected stale source error")
	}
	written, _ := os.ReadFile(path)
	if string(written) != "old" {
		t.Fatal("failed apply changed file")
	}
}

func TestApplyAddsActionVersionComment(t *testing.T) {
	root := t.TempDir()
	oldDigest := strings.Repeat("a", 40)
	newDigest := strings.Repeat("b", 40)
	content := "uses: actions/setup-go@" + oldDigest + "\n"
	path := filepath.Join(root, "action.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	start := strings.Index(content, oldDigest)
	report := Report{Updates: []Update{{
		Candidate:     Candidate{Manager: ManagerGitHubActions, Name: "actions/setup-go", CurrentVersion: "v6", CurrentValue: oldDigest, File: "action.yml", Line: 1, Start: start, End: start + len(oldDigest)},
		LatestVersion: "v7.0.0", LatestDigest: newDigest, Status: "outdated",
	}}}
	if _, err := Apply(root, report, true); err != nil {
		t.Fatal(err)
	}
	written, _ := os.ReadFile(path)
	if string(written) != "uses: actions/setup-go@"+newDigest+" # v7.0.0\n" {
		t.Fatalf("unexpected content: %q", written)
	}
}

func TestApplyRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside-go.mod")
	if err := os.WriteFile(outside, []byte("v1.0.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Report{Updates: []Update{{
		Candidate:     Candidate{Manager: ManagerGoMod, Name: "x", CurrentVersion: "v1.0.0", CurrentValue: "v1.0.0", File: "../outside-go.mod", Start: 0, End: 6},
		LatestVersion: "v1.1.0", Status: "outdated",
	}}}
	if _, err := Apply(root, report, true); err == nil {
		t.Fatal("path escape accepted")
	}
	written, _ := os.ReadFile(outside)
	if string(written) != "v1.0.0" {
		t.Fatal("outside file changed")
	}
}

func TestApplyValidatesEveryFileBeforeWriting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.mod"), []byte("v1.0.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.mod"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Report{Updates: []Update{
		{Candidate: Candidate{Manager: ManagerGoMod, Name: "a", CurrentVersion: "v1.0.0", CurrentValue: "v1.0.0", File: "a.mod", Start: 0, End: 6}, LatestVersion: "v1.1.0", Status: "outdated"},
		{Candidate: Candidate{Manager: ManagerGoMod, Name: "b", CurrentVersion: "v1.0.0", CurrentValue: "v1.0.0", File: "b.mod", Start: 0, End: 6}, LatestVersion: "v1.1.0", Status: "outdated"},
	}}
	if _, err := Apply(root, report, true); err == nil {
		t.Fatal("stale second file accepted")
	}
	first, _ := os.ReadFile(filepath.Join(root, "a.mod"))
	if string(first) != "v1.0.0" {
		t.Fatal("first file changed before full validation")
	}
}

func TestApplyRejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "go.mod"), []byte("module old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "nested")); err != nil {
		t.Fatal(err)
	}
	report := Report{Updates: []Update{{
		Candidate:     Candidate{Manager: ManagerGoMod, Name: "module", CurrentVersion: "old", CurrentValue: "old", File: "nested/go.mod", Start: 7, End: 10},
		LatestVersion: "new", Status: "outdated",
	}}}
	if _, err := Apply(root, report, true); err == nil || !strings.Contains(err.Error(), "symlink component") {
		t.Fatalf("error=%v", err)
	}
	data, err := os.ReadFile(filepath.Join(outside, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "module old\n" {
		t.Fatalf("outside file changed: %q", data)
	}
}

func TestWritePlansRejectsTargetMetadataChangedAfterPlanning(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
		want   string
	}{
		{
			name: "permissions",
			mutate: func(t *testing.T, root string) {
				if err := os.Chmod(filepath.Join(root, "sub", "package.json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "permissions changed",
		},
		{
			name: "symlink parent",
			mutate: func(t *testing.T, root string) {
				original := filepath.Join(root, "original")
				if err := os.Rename(filepath.Join(root, "sub"), original); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(original, filepath.Join(root, "sub")); err != nil {
					t.Fatal(err)
				}
			},
			want: "path contains symlink",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			content := `{"dependencies":{"demo":"1.0.0"}}`
			if err := os.WriteFile(filepath.Join(root, "sub", "package.json"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			start := strings.Index(content, "1.0.0")
			report := Report{Updates: []Update{{
				Candidate:     Candidate{Manager: ManagerNPM, Name: "demo", CurrentVersion: "1.0.0", CurrentValue: "1.0.0", File: "sub/package.json", Start: start, End: start + len("1.0.0")},
				LatestVersion: "1.1.0", Status: "outdated",
			}}}
			plans, err := planReport(root, report)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, root)
			if err := writePlans(plans); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("changed target accepted: %v", err)
			}
		})
	}
}
func TestApplyPreservesCargoConstraintOperator(t *testing.T) {
	root := t.TempDir()
	content := "[dependencies]\nfoo = \"=1.2.3\"\nbar = \"~1.2.3\"\n"
	path := filepath.Join(root, "Cargo.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	fooStart := strings.Index(content, "=1.2.3")
	barStart := strings.Index(content, "~1.2.3")
	report := Report{Updates: []Update{
		{Candidate: Candidate{Manager: ManagerCargo, Name: "foo", CurrentVersion: "1.2.3", CurrentValue: "=1.2.3", Prefix: "=", File: "Cargo.toml", Start: fooStart, End: fooStart + len("=1.2.3")}, LatestVersion: "1.2.4", Status: "outdated"},
		{Candidate: Candidate{Manager: ManagerCargo, Name: "bar", CurrentVersion: "1.2.3", CurrentValue: "~1.2.3", Prefix: "~", File: "Cargo.toml", Start: barStart, End: barStart + len("~1.2.3")}, LatestVersion: "1.3.0", Status: "outdated"},
	}}
	preview, err := Apply(root, report, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview) != 1 || string(preview[0].After) != "[dependencies]\nfoo = \"=1.2.4\"\nbar = \"~1.3.0\"\n" {
		t.Fatalf("unexpected preview: %#v", preview)
	}
}
func TestApplyActionFailsClosedForInvalidRanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "action.yml")
	content := "uses: actions/checkout@abcdef\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		start int
		end   int
		value string
	}{
		{"shortened source", 30, 70, "abcdef"},
		{"changed in bounds", strings.Index(content, "abcdef"), strings.Index(content, "abcdef") + len("abcdef"), "123456"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := Report{Updates: []Update{{Candidate: Candidate{
				Manager: ManagerGitHubActions, Name: "actions/checkout", CurrentValue: test.value,
				CurrentVersion: "v1", File: "action.yml", Start: test.start, End: test.end,
			}, LatestVersion: "v2", LatestDigest: strings.Repeat("b", 40), Status: "outdated"}}}
			if _, err := Apply(root, report, false); err == nil {
				t.Fatal("expected stale range error")
			}
			written, readErr := os.ReadFile(path)
			if readErr != nil || string(written) != content {
				t.Fatalf("source changed: %q (%v)", written, readErr)
			}
		})
	}
}
