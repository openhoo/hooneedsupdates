package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncReleaseVersion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("2.3.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readme := strings.Join([]string{
		"tool@v0.1.1",
		"ghcr.io/openhoo/hooneedsupdates:v0.1.1 scan .",
		"action@SHA # v0.1.1",
		"    version: 0.1.1",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := syncReleaseVersion(root); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(updated), "2.3.4") != 4 || strings.Contains(string(updated), "0.1.1") {
		t.Fatalf("unexpected README:\n%s", updated)
	}
}

func TestSyncReleaseVersionRejectsInvalidVersion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("v1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syncReleaseVersion(root); err == nil {
		t.Fatal("expected invalid VERSION to fail")
	}
}
