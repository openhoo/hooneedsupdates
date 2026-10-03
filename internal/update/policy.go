package update

import (
	"fmt"
	"github.com/openhoo/hooneedsupdates/internal/config"
	"golang.org/x/mod/semver"
	"time"
)

func policyChannel(cfg config.Config, candidate Candidate) (bool, bool, string) {
	prereleases, needPublished := cfg.IncludePrereleases, false
	channel, group := "", ""
	for _, rule := range cfg.MatchingRules(string(candidate.Manager), candidate.Name) {
		if rule.Channel != "" {
			if channel != "" && channel != rule.Channel {
				return false, false, "conflicting release channels"
			}
			channel = rule.Channel
			prereleases = channel == "prerelease"
		}
		if rule.Group != "" {
			if group != "" && group != rule.Group {
				return false, false, "conflicting update groups"
			}
			group = rule.Group
		}
		if rule.MinimumAge != "" {
			age, _ := time.ParseDuration(rule.MinimumAge)
			needPublished = needPublished || age > 0
		}
	}
	return prereleases, needPublished, ""
}
func applyPackagePolicy(cfg config.Config, entry Update, now time.Time) Update {
	rules := cfg.MatchingRules(string(entry.Manager), entry.Name)
	for _, rule := range rules {
		if rule.Group != "" {
			entry.Group = rule.Group
		}
	}
	if entry.Status != "outdated" {
		return entry
	}
	for _, rule := range rules {
		if rule.MinimumAge != "" {
			age, _ := time.ParseDuration(rule.MinimumAge)
			if age > 0 && (entry.PublishedAt == nil || entry.PublishedAt.IsZero() || now.Sub(*entry.PublishedAt) < age) {
				entry.Status, entry.Error = "blocked", "target publication age is missing or below minimumAge"
				return entry
			}
		}
		target := normalizeVersion(entry.LatestVersion)
		if (rule.MinVersion != "" && (target == "" || semver.Compare(target, normalizeVersion(rule.MinVersion)) < 0)) || (rule.MaxVersion != "" && (target == "" || semver.Compare(target, normalizeVersion(rule.MaxVersion)) > 0)) {
			entry.Status, entry.Error = "blocked", "newest target is outside the configured compatibility window"
			return entry
		}
	}
	return entry
}
func enforceGroups(cfg config.Config, updates []Update) {
	groups := map[string][]int{}
	for i, entry := range updates {
		if entry.Group != "" {
			groups[entry.Group] = append(groups[entry.Group], i)
		}
	}
	for name, members := range groups {
		shared := false
		for _, i := range members {
			for _, rule := range cfg.MatchingRules(string(updates[i].Manager), updates[i].Name) {
				shared = shared || rule.SharedVersion
			}
		}
		reason, target := "", ""
		for _, i := range members {
			entry := updates[i]
			if entry.Status == "unresolved" || entry.Status == "unsupported" || entry.Status == "blocked" || entry.Status == "ignored" {
				reason = "group contains an unavailable or excluded member"
			}
			if shared {
				v := normalizeVersion(entry.LatestVersion)
				if v == "" || (target != "" && target != v) {
					reason = "shared-version targets disagree or cannot be compared"
				}
				target = v
			}
		}
		if reason != "" {
			for _, i := range members {
				if updates[i].Status == "outdated" || updates[i].Status == "current" {
					updates[i].Status = "blocked"
					updates[i].Error = fmt.Sprintf("group %s: %s", name, reason)
				}
			}
		}
	}
}
