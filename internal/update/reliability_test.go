package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/hooneedsupdates/internal/config"
	"github.com/openhoo/hooneedsupdates/internal/githubapi"
)

func TestScannerRejectsInvalidConcurrencyWithoutHanging(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "package.json", `{"dependencies":{"demo":"1.0.0"}}`)
	for _, concurrency := range []int{0, -1, 33} {
		cfg := config.Default()
		cfg.Concurrency = concurrency
		_, err := (Scanner{Config: cfg, Resolver: resolverFunc(func(context.Context, Candidate, bool) (Resolution, error) {
			t.Fatal("invalid config reached resolver")
			return Resolution{}, nil
		})}).Scan(context.Background(), root)
		if err == nil || !strings.Contains(err.Error(), "concurrency") {
			t.Fatalf("concurrency=%d: %v", concurrency, err)
		}
	}
}

func TestScannerCancellationNeverReturnsPartialReport(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "package.json", `{"dependencies":{"a":"1.0.0","b":"1.0.0","c":"1.0.0"}}`)
	cfg := config.Default()
	cfg.Concurrency = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	resolver := resolverFunc(func(ctx context.Context, _ Candidate, _ bool) (Resolution, error) {
		calls++
		cancel()
		return Resolution{}, ctx.Err()
	})
	report, err := (Scanner{Config: cfg, Resolver: resolver}).Scan(ctx, root)
	if !errors.Is(err, context.Canceled) || len(report.Updates) != 0 || calls != 1 {
		t.Fatalf("report=%+v calls=%d error=%v", report, calls, err)
	}
}

func TestExtractorRejectsOversizedManifestButSkipsUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "large.txt", strings.Repeat("x", maxManifestSize+1))
	extractor := Extractor{Root: root, Config: config.Default()}
	if _, err := extractor.Extract(); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "package.json", strings.Repeat(" ", maxManifestSize+1))
	if _, err := extractor.Extract(); err == nil || !strings.Contains(err.Error(), "5 MiB") {
		t.Fatalf("error=%v", err)
	}
}

func TestExtractorPrunesExcludedDirectory(t *testing.T) {
	root := t.TempDir()
	// If only files are filtered, this pattern does not exclude the invalid child.
	writeFixture(t, root, "ignored/package.json", "invalid JSON")
	cfg := config.Default()
	cfg.ExcludePaths = []string{"^ignored/$"}
	if entries, err := (Extractor{Root: root, Config: cfg}).Extract(); err != nil || len(entries) != 0 {
		t.Fatalf("entries=%+v error=%v", entries, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Extractor{Root: root, Config: cfg}).ExtractContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestRepositoryRootAcceptsAliasButRejectsSubdirectory(t *testing.T) {
	root := gitFixture(t, map[string]string{"README.md": "fixture"})
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	got, err := repositoryRoot(alias)
	if err != nil || got != alias {
		t.Fatalf("root=%q error=%v", got, err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := repositoryRoot(nested); err == nil {
		t.Fatal("nested directory accepted as repository root")
	}
}

func TestCargoZeroPatchAndPrereleaseCompatibility(t *testing.T) {
	for _, tc := range []struct {
		requirement, latest string
		allowed             bool
	}{
		{"0.0.3", "0.0.4", false}, {"^0.0.3", "0.0.3", true},
		{"0.0", "0.0.9", true}, {"0", "0.9.0", true},
		{"~0.0.3", "0.0.4", true}, {"^1.2.3", "1.3.0-beta.1", false},
		{"^1.2.3-alpha.1", "1.2.3-beta.1", true},
		{"^1.2.3-alpha.1", "1.3.0-beta.1", false},
		{"^0.0.3-alpha.1", "0.0.3", true},
	} {
		got := constraintAllowsLatest(Candidate{Manager: ManagerCargo, CurrentValue: tc.requirement}, tc.latest)
		if got != tc.allowed {
			t.Errorf("%s -> %s: %v, want %v", tc.requirement, tc.latest, got, tc.allowed)
		}
	}
}

func TestVersionNormalizationRejectsMalformedSuffixes(t *testing.T) {
	for _, value := range []string{"1.2.3junk", "1.2.3.4", "1.2.3/evil", "1.2.3-", "1.2.3+"} {
		if got := normalizeVersion(value); got != "" {
			t.Errorf("%q normalized to %q", value, got)
		}
	}
	if got := normalizeVersion("1.2.3+build.7"); got != "v1.2.3+build.7" {
		t.Fatalf("metadata=%q", got)
	}
}

func TestNPMLatestMustBeStableAndValid(t *testing.T) {
	for _, version := range []string{"2.0.0-beta.1", "2.0.0garbage", "not-a-version"} {
		t.Run(version, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"version":%q}`, version) }))
			defer server.Close()
			resolver := NewHTTPResolver(server.Client(), "")
			resolver.NPMRegistry = server.URL
			if _, err := resolver.resolveNPM(context.Background(), "demo", false); err == nil {
				t.Fatal("unsafe latest version accepted")
			}
		})
	}
}

func TestDockerPaginationFindsVersionBeyondFifthPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "6" {
			fmt.Fprint(w, `{"results":[{"name":"3.25.0"}]}`)
			return
		}
		page := 1
		if value := r.URL.Query().Get("page"); value != "" {
			fmt.Sscan(value, &page)
		}
		fmt.Fprintf(w, `{"next":"?page=%d","results":[{"name":"3.24.0"}]}`, page+1)
	}))
	defer server.Close()
	resolver := NewHTTPResolver(server.Client(), "")
	resolver.DockerHub = server.URL
	got, err := resolver.resolveDocker(context.Background(), "alpine", "3.22.0", false)
	if err != nil || got.Version != "3.25.0" {
		t.Fatalf("resolution=%+v error=%v", got, err)
	}
}

func TestDockerPaginationFailsClosed(t *testing.T) {
	for _, tc := range []struct{ next, want string }{
		{"https://other.example/tags", "leaves"}, {"?page=1", "cycle"}, {"?page=", "page limit"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			page := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page++
				next := tc.next
				if tc.want == "page limit" {
					next = fmt.Sprintf("?page=%d", page+1)
				}
				fmt.Fprintf(w, `{"next":%q,"results":[{"name":"1.0.0"}]}`, next)
			}))
			defer server.Close()
			resolver := NewHTTPResolver(&http.Client{Timeout: time.Second}, "")
			if _, err := resolver.dockerTags(context.Background(), server.URL+"?page=1"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestLockfilesIgnoreInheritedGitRepositorySelection(t *testing.T) {
	root := gitFixture(t, map[string]string{"go.mod": "module example.test/demo\n\ngo 1.25\n"})
	other := gitFixture(t, map[string]string{"README.md": "other"})
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	if got, err := repositoryRoot(root); err != nil || got != root {
		t.Fatalf("root=%q error=%v", got, err)
	}
}

func TestActionsIgnoreScriptTextAndKeepVersionInputsWithinStep(t *testing.T) {
	data := []byte(`steps:
  - uses: openhoo/hoolicy/actions/check@v0.1.0
  - id: unrelated
    run: |
      uses: evil/action@v9.0.0
    with:
      version: 8.0.0
  - uses: openhoo/hooversion/actions/lint@v1.0.0
    with:
      version: 1.0.0
  - run: |
      uses: evil/other@v9.0.0
      version: 9.0.0
`)
	entries, err := extractActions(".github/workflows/ci.yml", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries=%+v", entries)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, "evil/") || (entry.Manager == ManagerCustom && entry.Name != "openhoo/hooversion") {
			t.Fatalf("unrelated text extracted: %+v", entry)
		}
		if string(data[entry.Start:entry.End]) != entry.CurrentValue {
			t.Fatalf("incorrect byte span: %+v", entry)
		}
	}
}

func TestActionsFindReusableWorkflowsAndCompositeSteps(t *testing.T) {
	for _, tc := range []struct{ data, want string }{
		{"jobs:\n  reuse:\n    uses: owner/repo/.github/workflows/ci.yml@v1.0.0\n", "owner/repo"},
		{"runs:\n  using: composite\n  steps:\n    - uses: owner/action@v1.0.0\n", "owner/action"},
	} {
		entries, err := extractActions("action.yml", []byte(tc.data))
		if err != nil || len(entries) != 1 || entries[0].Name != tc.want {
			t.Fatalf("entries=%+v error=%v", entries, err)
		}
	}
}

func TestActionsRejectInvalidOrMultipleYAMLDocuments(t *testing.T) {
	for _, data := range []string{"steps: [invalid", "steps: []\n---\nsteps: []\n"} {
		if _, err := extractActions("action.yml", []byte(data)); err == nil {
			t.Fatalf("invalid workflow accepted: %q", data)
		}
	}
}

func TestGoExtractionOnlyIncludesDirectRequirements(t *testing.T) {
	data := []byte("module example.test/demo\n\ngo 1.25\n\nrequire (\n example.test/direct v1.0.0\n example.test/indirect v1.0.0 // indirect\n)\n")
	entries, err := extractGoMod("go.mod", data)
	if err != nil || len(entries) != 1 || entries[0].Name != "example.test/direct" {
		t.Fatalf("entries=%+v error=%v", entries, err)
	}
}

func TestCargoExtractionDoesNotResolveGitOrAlternateRegistryAsCratesIO(t *testing.T) {
	entries := extractCargo("Cargo.toml", []byte(`[dependencies]
normal = "1.0.0"
gitdep = { git = "https://example.test/repo", version = "1.0.0" }
private = { registry = "private", version = "1.0.0" }
`))
	if len(entries) != 1 || entries[0].Name != "normal" {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestExtractorScansExplicitRootAliasAndRejectsFileRoot(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "package.json", `{"dependencies":{"demo":"1.0.0"}}`)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if entries, err := (Extractor{Root: alias, Config: config.Default()}).Extract(); err != nil || len(entries) != 1 {
		t.Fatalf("entries=%+v error=%v", entries, err)
	}
	if _, err := (Extractor{Root: filepath.Join(root, "package.json"), Config: config.Default()}).Extract(); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("file root accepted: %v", err)
	}
}

func TestNPMAndNuGetDiscoverPrereleaseAndBuildVersions(t *testing.T) {
	for _, version := range []string{"1.2.3-beta.1", "1.2.3+build.7", "1.2.3-beta.1+build.7"} {
		entries, err := extractNPM("package.json", []byte(fmt.Sprintf(`{"dependencies":{"demo":%q}}`, "^"+version)))
		if err != nil || len(entries) != 1 || entries[0].CurrentVersion != version || entries[0].Prefix != "^" {
			t.Fatalf("npm entries=%+v error=%v", entries, err)
		}
		entries, err = extractNuGet("demo.csproj", []byte(fmt.Sprintf(`<Project><ItemGroup><PackageReference Include="Demo" Version="%s" /></ItemGroup></Project>`, version)))
		if err != nil || len(entries) != 1 || entries[0].CurrentVersion != version {
			t.Fatalf("nuget entries=%+v error=%v", entries, err)
		}
	}
}

func TestGitHubTagFallbackPreservesRateLimitDeferral(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"message":"secondary rate limit"}`)
	}))
	defer server.Close()
	resolver := NewHTTPResolver(server.Client(), "")
	resolver.GitHubAPI = server.URL
	client, err := githubapi.New(server.Client(), server.URL, githubapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver.GitHubClient = client
	_, err = resolver.latestGitHubTag(context.Background(), "owner/action", false)
	var limited *githubapi.RateLimitError
	if !errors.As(err, &limited) || calls != 2 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestGitHubReleaseFailureDoesNotFallBackToTags(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"bad credentials"}`)
	}))
	defer server.Close()
	resolver := NewHTTPResolver(server.Client(), "")
	resolver.GitHubAPI = server.URL
	if _, err := resolver.latestGitHubTag(context.Background(), "owner/action", false); err == nil || !strings.Contains(err.Error(), "401") || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
