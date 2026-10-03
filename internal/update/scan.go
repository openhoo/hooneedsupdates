package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openhoo/hooneedsupdates/internal/config"
	"github.com/openhoo/hooneedsupdates/internal/githubapi"
)

type Scanner struct {
	Config   config.Config
	Resolver Resolver
	Now      func() time.Time
}

func (s Scanner) Scan(ctx context.Context, root string) (Report, error) {
	if s.Resolver == nil {
		return Report{}, fmt.Errorf("resolver is required")
	}
	if err := s.Config.Validate(); err != nil {
		return Report{}, fmt.Errorf("invalid scanner configuration: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Report{}, err
	}
	candidates, err := (Extractor{Root: absRoot, Config: s.Config}).ExtractContext(ctx)
	if err != nil {
		return Report{}, err
	}
	updates := make([]Update, len(candidates))
	type job struct{ index int }
	jobs := make(chan job)
	workerCount := s.Config.Concurrency
	if workerCount > len(candidates) {
		workerCount = len(candidates)
	}
	var workers sync.WaitGroup
	cache := map[string]*resolutionResult{}
	var cacheMu sync.Mutex
	var fatalErr error
	var fatalMu sync.Mutex
	resolveCached := func(candidate Candidate) (Resolution, error) {
		prereleases, _, _ := policyChannel(s.Config, candidate)
		key := strings.Join([]string{candidate.Datasource, candidate.Name, candidate.CurrentVersion, candidate.CurrentDigest, fmt.Sprint(candidate.NeedPublished), fmt.Sprint(prereleases)}, "\x00")
		cacheMu.Lock()
		if existing, ok := cache[key]; ok {
			cacheMu.Unlock()
			select {
			case <-existing.done:
				return existing.resolution, existing.err
			case <-ctx.Done():
				return Resolution{}, ctx.Err()
			}
		}
		pending := &resolutionResult{done: make(chan struct{})}
		cache[key] = pending
		cacheMu.Unlock()
		resolution, resolveErr := s.Resolver.Resolve(ctx, candidate, prereleases)
		cacheMu.Lock()
		pending.resolution, pending.err = resolution, resolveErr
		close(pending.done)
		cacheMu.Unlock()
		return resolution, resolveErr
	}
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for task := range jobs {
				if ctx.Err() != nil {
					continue
				}
				candidate := candidates[task.index]
				if reason := s.Config.IgnoreReason(string(candidate.Manager), candidate.Name); reason != "" {
					updates[task.index] = Update{Candidate: candidate, Status: "ignored", Error: reason}
					continue
				}
				if candidate.UnsupportedReason != "" {
					updates[task.index] = Update{Candidate: candidate, Status: "unsupported", Error: candidate.UnsupportedReason}
					continue
				}
				_, needPublished, policyErr := policyChannel(s.Config, candidate)
				if policyErr != "" {
					updates[task.index] = Update{Candidate: candidate, Status: "blocked", Error: policyErr}
					continue
				}
				candidate.NeedPublished = needPublished
				resolution, resolveErr := resolveCached(candidate)
				if resolveErr != nil {
					var limited *githubapi.RateLimitError
					if errors.As(resolveErr, &limited) {
						fatalMu.Lock()
						if fatalErr == nil || limited.RetryAt.After(rateLimitRetryAt(fatalErr)) {
							fatalErr = resolveErr
						}
						fatalMu.Unlock()
						continue
					}
					updates[task.index] = Update{Candidate: candidate, Status: "unresolved", Error: resolveErr.Error()}
					continue
				}
				updates[task.index] = classifyResolved(s.Config, candidate, resolution)
			}
		}()
	}
dispatch:
	for index := range candidates {
		select {
		case jobs <- job{index: index}:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if fatalErr != nil {
		return Report{}, fatalErr
	}

	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	nowTime := now().UTC()
	for i, entry := range updates {
		updates[i] = applyPackagePolicy(s.Config, entry, nowTime)
	}
	enforceGroups(s.Config, updates)
	sort.SliceStable(updates, func(i, j int) bool {
		if updates[i].Status != updates[j].Status {
			return statusOrder(updates[i].Status) < statusOrder(updates[j].Status)
		}
		if updates[i].File != updates[j].File {
			return updates[i].File < updates[j].File
		}
		return updates[i].Line < updates[j].Line
	})

	report := Report{
		SchemaVersion: 2,
		GeneratedAt:   nowTime,
		Root:          filepath.ToSlash(absRoot),
		PlanDigest:    planDigest(updates),
		Updates:       updates,
	}
	report.Summary.Detected = len(updates)
	for _, entry := range updates {
		switch entry.Status {
		case "current":
			report.Summary.Current++
		case "outdated":
			report.Summary.Outdated++
		case "unresolved":
			report.Summary.Unresolved++
		case "ignored":
			report.Summary.Ignored++
		case "blocked":
			report.Summary.Blocked++
		case "unsupported":
			report.Summary.Unsupported++
		default:
		}
	}
	return report, nil
}

func rateLimitRetryAt(err error) time.Time {
	var limited *githubapi.RateLimitError
	if errors.As(err, &limited) {
		return limited.RetryAt
	}
	return time.Time{}
}

// FilterReport returns a new, internally consistent report containing only
// entries accepted by keep. Its summary and plan digest are recomputed so the
// result remains safe to pass to Apply or ApplyWithLockfiles.
func FilterReport(report Report, keep func(Update) bool) Report {
	filtered := report
	filtered.Updates = make([]Update, 0, len(report.Updates))
	filtered.Summary = Summary{}
	excludedGroups := map[string]bool{}
	for _, entry := range report.Updates {
		if entry.Group != "" && !keep(entry) {
			excludedGroups[entry.Group] = true
		}
	}
	for _, entry := range report.Updates {
		if excludedGroups[entry.Group] && entry.Group != "" {
			entry.Status = "blocked"
			entry.Error = "selection would split update group"
		} else if !keep(entry) {
			continue
		}
		filtered.Updates = append(filtered.Updates, entry)
		filtered.Summary.Detected++
		switch entry.Status {
		case "current":
			filtered.Summary.Current++
		case "outdated":
			filtered.Summary.Outdated++
		case "unresolved":
			filtered.Summary.Unresolved++
		case "ignored":
			filtered.Summary.Ignored++
		case "blocked":
			filtered.Summary.Blocked++
		case "unsupported":
			filtered.Summary.Unsupported++
		}
	}
	filtered.PlanDigest = planDigest(filtered.Updates)
	return filtered
}

func planDigest(updates []Update) string {
	digest := sha256.New()
	writeDigestField(digest, "hooneedsupdates-plan-v2")
	for _, entry := range updates {
		if entry.Status != "outdated" {
			continue
		}
		for _, field := range []string{
			string(entry.Manager), entry.Datasource, entry.Name, entry.CurrentVersion,
			entry.CurrentValue, entry.File, fmt.Sprintf("%d", entry.Start),
			fmt.Sprintf("%d", entry.End), entry.Prefix, entry.Suffix,
			entry.LatestVersion, entry.LatestDigest, entry.UpdateType,
			entry.CurrentDigest, entry.Group,
		} {
			writeDigestField(digest, field)
		}
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func writeDigestField(digest hash.Hash, value string) {
	_, _ = fmt.Fprintf(digest, "%d:", len(value))
	_, _ = digest.Write([]byte(value))
}

func classifyResolved(cfg config.Config, candidate Candidate, resolution Resolution) Update {
	entry := Update{Candidate: candidate, LatestVersion: resolution.Version, LatestDigest: resolution.Digest, PublishedAt: resolution.PublishedAt}
	entry.UpdateType = updateType(candidate.CurrentVersion, resolution.Version)
	if current(candidate, resolution) || constraintAllowsLatest(candidate, resolution.Version) {
		entry.Status = "current"
		return entry
	}
	if !newer(candidate.CurrentVersion, resolution.Version) && !actionDigestChanged(candidate, resolution) && !dockerDigestChanged(candidate, resolution) {
		entry.Status = "current"
		return entry
	}
	if entry.UpdateType != "unknown" && !cfg.UpdateTypeAllowed(entry.UpdateType) {
		entry.Status, entry.Error = "ignored", "update type disabled by configuration"
		return entry
	}
	entry.Status = "outdated"
	return entry
}

type resolutionResult struct {
	resolution Resolution
	err        error
	done       chan struct{}
}

func current(candidate Candidate, resolution Resolution) bool {
	if candidate.Manager == ManagerDocker {
		return normalizeVersion(candidate.CurrentVersion) == normalizeVersion(resolution.Version) &&
			(candidate.CurrentDigest == "" || strings.EqualFold(candidate.CurrentDigest, resolution.Digest))
	}
	if resolution.Digest != "" {
		if !strings.EqualFold(candidate.CurrentValue, resolution.Digest) {
			return false
		}
		currentVersion := normalizeVersion(candidate.CurrentVersion)
		latestVersion := normalizeVersion(resolution.Version)
		return currentVersion == "" || (latestVersion != "" && currentVersion == latestVersion)
	}
	current := normalizeVersion(candidate.CurrentVersion)
	latest := normalizeVersion(resolution.Version)
	return current != "" && latest != "" && current == latest
}

func dockerDigestChanged(candidate Candidate, resolution Resolution) bool {
	return candidate.Manager == ManagerDocker && candidate.CurrentDigest != "" && resolution.Digest != "" &&
		!strings.EqualFold(candidate.CurrentDigest, resolution.Digest) && !newer(resolution.Version, candidate.CurrentVersion)
}

func actionDigestChanged(candidate Candidate, resolution Resolution) bool {
	if candidate.Manager != ManagerGitHubActions || resolution.Digest == "" ||
		strings.EqualFold(candidate.CurrentValue, resolution.Digest) {
		return false
	}
	// A stale resolver result must never turn digest pinning into a downgrade.
	if newer(resolution.Version, candidate.CurrentVersion) {
		return false
	}
	return true
}

func statusOrder(status string) int {
	switch status {
	case "outdated":
		return 0
	case "unresolved":
		return 1
	case "ignored":
		return 2
	default:
		return 3
	}
}
