package update

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openhoo/hooneedsupdates/internal/config"
)

type fakeResolver struct {
	mu    sync.Mutex
	calls map[string]int
}

func (f *fakeResolver) Resolve(_ context.Context, candidate Candidate, _ bool) (Resolution, error) {
	f.mu.Lock()
	f.calls[candidate.Name]++
	f.mu.Unlock()
	return Resolution{Version: "2.0.0"}, nil
}

func TestScannerCachesAndHonorsIgnoreRules(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "package.json", `{"dependencies":{"same":"1.0.0","ignored":"1.0.0"}}`)
	writeFixture(t, root, "nested/package.json", `{"devDependencies":{"same":"1.0.0"}}`)
	cfg := config.Default()
	cfg.Managers = []string{"npm"}
	cfg.Ignore = []config.IgnoreRule{{Dependency: "^ignored$", Reason: "fixture"}}
	resolver := &fakeResolver{calls: map[string]int{}}
	report, err := (Scanner{Config: cfg, Resolver: resolver, Now: func() time.Time { return time.Unix(0, 0) }}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Outdated != 2 || report.Summary.Ignored != 1 || report.Summary.Detected != 3 {
		t.Fatalf("unexpected summary: %#v", report.Summary)
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if resolver.calls["same"] != 1 || resolver.calls["ignored"] != 0 {
		t.Fatalf("unexpected resolver calls: %#v", resolver.calls)
	}
}

func TestPinnedActionWithStaleCommentNeedsUpdate(t *testing.T) {
	digest := "0123456789012345678901234567890123456789"
	candidate := Candidate{Manager: ManagerGitHubActions, CurrentValue: digest, CurrentVersion: "v4"}
	resolution := Resolution{Version: "v4.2.0", Digest: digest}
	if current(candidate, resolution) {
		t.Fatal("stale action version comment treated as current")
	}
	candidate.CurrentVersion = digest
	if !current(candidate, resolution) {
		t.Fatal("commentless current digest treated as outdated")
	}
}

func TestFilterReportRecomputesSummaryAndDigestWithoutMutatingSource(t *testing.T) {
	report := Report{
		SchemaVersion: 2,
		PlanDigest:    "original",
		Summary:       Summary{Detected: 3, Current: 1, Outdated: 1, Unresolved: 1},
		Updates: []Update{
			{Candidate: Candidate{Name: "keep", Manager: ManagerGoMod}, LatestVersion: "v2.0.0", UpdateType: "major", Status: "outdated"},
			{Candidate: Candidate{Name: "drop", Manager: ManagerGoMod}, Status: "current"},
			{Candidate: Candidate{Name: "keep-unresolved", Manager: ManagerCustom}, Status: "unresolved", Error: "offline"},
		},
	}
	filtered := FilterReport(report, func(entry Update) bool { return strings.HasPrefix(entry.Name, "keep") })
	if len(filtered.Updates) != 2 || filtered.Summary != (Summary{Detected: 2, Outdated: 1, Unresolved: 1}) {
		t.Fatalf("filtered=%+v", filtered)
	}
	if filtered.PlanDigest == "" || filtered.PlanDigest == report.PlanDigest {
		t.Fatalf("digest=%q", filtered.PlanDigest)
	}
	if len(report.Updates) != 3 || report.PlanDigest != "original" {
		t.Fatalf("source report mutated: %+v", report)
	}
}
