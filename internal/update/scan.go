package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openhoo/hooneedsupdates/internal/config"
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
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Report{}, err
	}
	candidates, err := (Extractor{Root: absRoot, Config: s.Config}).Extract()
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
	resolveCached := func(candidate Candidate) (Resolution, error) {
		key := strings.Join([]string{candidate.Datasource, candidate.Name, candidate.CurrentVersion}, "\x00")
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
		resolution, resolveErr := s.Resolver.Resolve(ctx, candidate, s.Config.IncludePrereleases)
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
				candidate := candidates[task.index]
				if reason := s.Config.IgnoreReason(string(candidate.Manager), candidate.Name); reason != "" {
					updates[task.index] = Update{Candidate: candidate, Status: "ignored", Error: reason}
					continue
				}
				resolution, resolveErr := resolveCached(candidate)
				if resolveErr != nil {
					updates[task.index] = Update{Candidate: candidate, Status: "unresolved", Error: resolveErr.Error()}
					continue
				}
				updates[task.index] = classifyResolved(s.Config, candidate, resolution)
			}
		}()
	}
	for index := range candidates {
		jobs <- job{index: index}
	}
	close(jobs)
	workers.Wait()

	sort.SliceStable(updates, func(i, j int) bool {
		if updates[i].Status != updates[j].Status {
			return statusOrder(updates[i].Status) < statusOrder(updates[j].Status)
		}
		if updates[i].File != updates[j].File {
			return updates[i].File < updates[j].File
		}
		return updates[i].Line < updates[j].Line
	})
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	report := Report{
		SchemaVersion: 2,
		GeneratedAt:   now().UTC(),
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
		default:
		}
	}
	return report, nil
}

func planDigest(updates []Update) string {
	digest := sha256.New()
	writeDigestField(digest, "hooneedsupdates-plan-v1")
	for _, entry := range updates {
		if entry.Status != "outdated" {
			continue
		}
		for _, field := range []string{
			string(entry.Manager), entry.Datasource, entry.Name, entry.CurrentVersion,
			entry.CurrentValue, entry.File, fmt.Sprintf("%d", entry.Start),
			fmt.Sprintf("%d", entry.End), entry.Prefix, entry.Suffix,
			entry.LatestVersion, entry.LatestDigest, entry.UpdateType,
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
	entry := Update{Candidate: candidate, LatestVersion: resolution.Version, LatestDigest: resolution.Digest}
	entry.UpdateType = updateType(candidate.CurrentVersion, resolution.Version)
	if current(candidate, resolution) || constraintAllowsLatest(candidate, resolution.Version) {
		entry.Status = "current"
		return entry
	}
	if !newer(candidate.CurrentVersion, resolution.Version) && !actionDigestChanged(candidate, resolution) {
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
	if resolution.Digest != "" && strings.EqualFold(candidate.CurrentValue, resolution.Digest) {
		currentVersion := normalizeVersion(candidate.CurrentVersion)
		latestVersion := normalizeVersion(resolution.Version)
		return currentVersion == "" || (latestVersion != "" && currentVersion == latestVersion)
	}
	current := normalizeVersion(candidate.CurrentVersion)
	latest := normalizeVersion(resolution.Version)
	return current != "" && latest != "" && current == latest
}

func actionDigestChanged(candidate Candidate, resolution Resolution) bool {
	return candidate.Manager == ManagerGitHubActions && resolution.Digest != "" &&
		len(candidate.CurrentValue) == 40 && !strings.EqualFold(candidate.CurrentValue, resolution.Digest)
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
