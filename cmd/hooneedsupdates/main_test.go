package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
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

func TestReportedVersionUsesGoModuleMetadata(t *testing.T) {
	previousVersion := version
	previousReadBuildInfo := readBuildInfo
	defer func() {
		version = previousVersion
		readBuildInfo = previousReadBuildInfo
	}()

	version = "dev"
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v0.1.1"}}, true
	}
	if got := reportedVersion(); got != "0.1.1" {
		t.Fatalf("reportedVersion() = %q, want 0.1.1", got)
	}

	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true
	}
	if got := reportedVersion(); got != "dev" {
		t.Fatalf("development reportedVersion() = %q, want dev", got)
	}

	version = "1.2.3-linked"
	if got := reportedVersion(); got != "1.2.3-linked" {
		t.Fatalf("linked reportedVersion() = %q, want linker override", got)
	}
}

func TestRunInitCreatesStrictConfigurationOnce(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"init", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr.String())
	}
	cfg, _, err := config.Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	workflow := filepath.Join(root, ".github", "workflows", "version.yml")
	if err := os.MkdirAll(filepath.Dir(workflow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflow, []byte(`env:
  HOOVERSION_VERSION: "1.2.3"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	candidates, err := (update.Extractor{Root: root, Config: cfg}).Extract()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Name != "openhoo/hooversion" || candidates[0].CurrentVersion != "1.2.3" {
		t.Fatalf("unexpected init candidates: %#v", candidates)
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

func TestUpdateReposRejectsMissingInventoryAndWriteTokenBeforeNetwork(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"update-repos"}, &stdout, &stderr); code != 2 ||
		!strings.Contains(stderr.String(), "requires owner/repository") {
		t.Fatalf("missing inventory code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(
		context.Background(),
		[]string{"update-repos", "--write", "openhoo/tool"},
		&stdout,
		&stderr,
	); code != 1 || !strings.Contains(stderr.String(), "GH_TOKEN") {
		t.Fatalf("missing token code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(
		context.Background(),
		[]string{"update-repos", "--format", "xml", "openhoo/tool"},
		&stdout,
		&stderr,
	); code != 2 || !strings.Contains(stderr.String(), "unknown format") {
		t.Fatalf("invalid format code=%d stderr=%q", code, stderr.String())
	}
}
func TestSelectedGitHubTokenPrecedence(t *testing.T) {
	t.Setenv("GH_TOKEN", "gh")
	t.Setenv("GITHUB_TOKEN", "github")
	if got := selectedGitHubToken(); got != "gh" {
		t.Fatalf("selected token=%q", got)
	}
	t.Setenv("GH_TOKEN", "")
	if got := selectedGitHubToken(); got != "github" {
		t.Fatalf("fallback token=%q", got)
	}
}

func TestScanUsesSelectedGitHubTokenAtResolverEndpoint(t *testing.T) {
	const commit = "0123456789012345678901234567890123456789"
	for _, test := range []struct {
		name, ghToken, githubToken, want string
	}{
		{"GH_TOKEN takes precedence", "preferred", "fallback", "preferred"},
		{"GITHUB_TOKEN fallback", "", "fallback", "fallback"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var authorization []string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				authorization = append(authorization, request.Header.Get("Authorization"))
				writer.Header().Set("Content-Type", "application/json")
				switch request.URL.Path {
				case "/repos/owner/action/releases/latest":
					_, _ = writer.Write([]byte(`{"tag_name":"v1.1.0"}`))
				case "/repos/owner/action/git/ref/tags/v1.1.0":
					_, _ = writer.Write([]byte(`{"object":{"type":"commit","sha":"` + commit + `"}}`))
				default:
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()

			root := t.TempDir()
			workflow := filepath.Join(root, ".github", "workflows", "ci.yml")
			if err := os.MkdirAll(filepath.Dir(workflow), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(workflow, []byte("steps:\n  - uses: owner/action@v1.0.0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GITHUB_API_URL", server.URL)
			t.Setenv("GH_TOKEN", test.ghToken)
			t.Setenv("GITHUB_TOKEN", test.githubToken)
			report, _, err := scan(context.Background(), root, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Updates) != 1 || report.Updates[0].Status != "outdated" {
				t.Fatalf("unexpected report: %+v", report)
			}
			if len(authorization) != 2 {
				t.Fatalf("observed %d GitHub requests, want 2", len(authorization))
			}
			for _, header := range authorization {
				if header != "Bearer "+test.want {
					t.Errorf("Authorization=%q, want Bearer %s", header, test.want)
				}
			}
		})
	}
}

func TestRunScanRejectsInvalidOptionsBeforeScan(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"format", []string{"scan", "--format", "xml", root}, `unknown format "xml"`},
		{"fail-on", []string{"scan", "--fail-on", "current", root}, `unknown --fail-on value "current"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), test.args, &stdout, &stderr); code != 2 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if stderr.String() != test.want+"\n" || stdout.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}
