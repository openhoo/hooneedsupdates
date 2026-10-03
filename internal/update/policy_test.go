package update

import (
	"context"
	"github.com/openhoo/hooneedsupdates/internal/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPackagePolicyAgeWindowAndGroupClosure(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	old, young := now.Add(-72*time.Hour), now.Add(-time.Hour)
	cfg := config.Default()
	cfg.PackageRules = []config.PackageRule{{Dependency: "^family/", Group: "family", SharedVersion: true, MinimumAge: "48h", Channel: "stable", MinVersion: "1.0.0", MaxVersion: "2.0.0"}}
	makeEntry := func(name, version string, published *time.Time) Update {
		return Update{Candidate: Candidate{Manager: ManagerNPM, Name: name}, Status: "outdated", LatestVersion: version, PublishedAt: published}
	}
	for _, entry := range []Update{makeEntry("family/a", "1.1.0", nil), makeEntry("family/a", "1.1.0", &young), makeEntry("family/a", "3.0.0", &old)} {
		if got := applyPackagePolicy(cfg, entry, now); got.Status != "blocked" {
			t.Fatalf("policy allowed %+v", got)
		}
	}
	updates := []Update{applyPackagePolicy(cfg, makeEntry("family/a", "1.1.0", &old), now), applyPackagePolicy(cfg, makeEntry("family/b", "1.2.0", &old), now)}
	enforceGroups(cfg, updates)
	for _, entry := range updates {
		if entry.Status != "blocked" {
			t.Fatalf("split family allowed %+v", entry)
		}
	}
	updates = []Update{applyPackagePolicy(cfg, makeEntry("family/a", "1.1.0", &old), now), applyPackagePolicy(cfg, makeEntry("family/b", "1.1.0", &old), now)}
	enforceGroups(cfg, updates)
	filtered := FilterReport(Report{Updates: updates}, func(entry Update) bool { return entry.Name == "family/a" })
	if filtered.Summary.Blocked != 2 || len(filtered.Updates) != 2 || RequireComplete(filtered) == nil {
		t.Fatalf("partial selection allowed %+v", filtered)
	}
}
func TestPolicyChannelConflictAndStableOverride(t *testing.T) {
	cfg := config.Default()
	cfg.IncludePrereleases = true
	cfg.PackageRules = []config.PackageRule{{Dependency: "a", Channel: "stable"}}
	pre, _, err := policyChannel(cfg, Candidate{Name: "a"})
	if pre || err != "" {
		t.Fatalf("stable override %v %s", pre, err)
	}
	cfg.PackageRules = append(cfg.PackageRules, config.PackageRule{Dependency: "a", Channel: "prerelease"})
	if _, _, err := policyChannel(cfg, Candidate{Name: "a"}); err == "" {
		t.Fatal("conflicting channels accepted")
	}
}

type publicationResolver struct{ now time.Time }

func (r publicationResolver) Resolve(_ context.Context, c Candidate, pre bool) (Resolution, error) {
	if !c.NeedPublished || pre {
		return Resolution{}, nil
	}
	return Resolution{Version: "1.1.0", PublishedAt: &r.now}, nil
}
func TestScannerAppliesPolicyWithRequiredMetadata(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"dependencies":{"a":"1.0.0"}}`), 0644)
	now := time.Now().UTC()
	cfg := config.Default()
	cfg.PackageRules = []config.PackageRule{{Dependency: "^a$", MinimumAge: "48h", Group: "family"}}
	report, err := (Scanner{Config: cfg, Resolver: publicationResolver{now: now.Add(-time.Hour)}, Now: func() time.Time { return now }}).Scan(context.Background(), root)
	if err != nil || report.Summary.Blocked != 1 || report.Updates[0].Group != "family" {
		t.Fatalf("policy report %+v %v", report, err)
	}
}
