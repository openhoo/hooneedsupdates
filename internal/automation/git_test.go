package automation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/hooneedsupdates/internal/update"
)

func TestGitCommitAcceptsOnlyExactAppliedPaths(t *testing.T) {
	root := gitRepository(t)
	writeTestFile(t, root, "expected.txt", "updated\n")
	writeTestFile(t, root, "unexpected.txt", "surprise\n")
	vcs := gitVCS{}
	_, _, err := vcs.Commit(context.Background(), root, "chore: update", "Bot", "bot@example.com", []update.AppliedFile{{Path: "expected.txt"}})
	if err == nil || !strings.Contains(err.Error(), "unexpected.txt") {
		t.Fatalf("unexpected file was accepted: %v", err)
	}
}

func TestGitCommitIsDeterministicAndRejectsEscapingPaths(t *testing.T) {
	first := gitRepository(t)
	second := gitRepository(t)
	for _, root := range []string{first, second} {
		writeTestFile(t, root, "expected.txt", "updated\n")
	}
	vcs := gitVCS{}
	files := []update.AppliedFile{{Path: "expected.txt"}}
	firstSHA, paths, err := vcs.Commit(context.Background(), first, "chore: update", "Bot", "bot@example.com", files)
	if err != nil {
		t.Fatal(err)
	}
	secondSHA, _, err := vcs.Commit(context.Background(), second, "chore: update", "Bot", "bot@example.com", files)
	if err != nil {
		t.Fatal(err)
	}
	if firstSHA != secondSHA || len(paths) != 1 || paths[0] != "expected.txt" {
		t.Fatalf("commits are not deterministic: first=%s second=%s paths=%v", firstSHA, secondSHA, paths)
	}
	for _, path := range []string{"../outside", "/absolute", `windows\escape`} {
		if _, _, err := expectedPaths([]update.AppliedFile{{Path: path}}); err == nil {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
}

func TestParseStatusRejectsDeletionAndMalformedRecords(t *testing.T) {
	if paths, err := parseStatus([]byte(" M expected.txt\x00?? new.txt\x00")); err != nil || len(paths) != 2 {
		t.Fatalf("valid status rejected: paths=%v error=%v", paths, err)
	}
	for _, status := range [][]byte{[]byte(" D deleted.txt\x00"), []byte(" M unterminated"), []byte("bad\x00")} {
		if _, err := parseStatus(status); err == nil {
			t.Fatalf("unsafe status accepted: %q", status)
		}
	}
}

func gitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGitTest(t, root, "init", "-b", "main")
	runGitTest(t, root, "remote", "add", "origin", "https://github.test/openhoo/tool.git")
	writeTestFile(t, root, "expected.txt", "original\n")
	runGitTest(t, root, "add", "expected.txt")
	command := exec.Command("git", "-C", root, "commit", "-m", "initial")
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, output)
	}
	return root
}

func writeTestFile(t *testing.T, root, path, contents string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGitTest(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}
