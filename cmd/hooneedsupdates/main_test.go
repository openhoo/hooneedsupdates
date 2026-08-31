package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/hooneedsupdates/internal/config"
	"github.com/openhoo/hooneedsupdates/internal/update"
)

func TestRunHelpVersionAndUnknownCommand(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "preview-first") {
			t.Fatalf("run(%v) code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
	previous := version
	version = "1.2.3-test"
	defer func() { version = previous }()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &stdout, &stderr); code != 0 || stdout.String() != "hooneedsupdates 1.2.3-test\n" {
		t.Fatalf("version code=%d stdout=%q", code, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"unknown"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("unknown code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunInitCreatesStrictConfigurationOnce(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"init", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr.String())
	}
	if _, _, err := config.Load(root, ""); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"init", root}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("second init code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunScanRejectsInvalidConfigurationWithoutNetwork(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte("version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"scan", root}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "unsupported version") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestReportExitModes(t *testing.T) {
	report := update.Report{Summary: update.Summary{Outdated: 1, Unresolved: 1}}
	var stderr bytes.Buffer
	if got := reportExit(report, "never", &stderr); got != 0 {
		t.Fatalf("never=%d", got)
	}
	if got := reportExit(report, "outdated", &stderr); got != 2 {
		t.Fatalf("outdated=%d", got)
	}
	if got := reportExit(report, "unresolved", &stderr); got != 3 {
		t.Fatalf("unresolved=%d", got)
	}
	if got := reportExit(report, "invalid", &stderr); got != 2 {
		t.Fatalf("invalid=%d", got)
	}
}
