package update

import (
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

var numericVersion = regexp.MustCompile(`(?i)^v?(\d+(?:\.\d+){0,2})(.*)$`)
var constrainedVersion = regexp.MustCompile(`(?i)v?(\d+(?:\.\d+){0,2})`)

func normalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimLeft(value, "=~^<> ")
	match := numericVersion.FindStringSubmatch(value)
	if match == nil {
		return ""
	}
	parts := strings.Split(match[1], ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	version := "v" + strings.Join(parts, ".")
	if suffix := match[2]; suffix != "" {
		version += suffix
	}
	if !semver.IsValid(version) {
		return ""
	}
	return version
}

func newer(current, latest string) bool {
	c := normalizeVersion(current)
	l := normalizeVersion(latest)
	return c != "" && l != "" && semver.Compare(l, c) > 0
}

func updateType(current, latest string) string {
	c := normalizeVersion(current)
	l := normalizeVersion(latest)
	if c == "" || l == "" {
		return "unknown"
	}
	if semver.Major(c) != semver.Major(l) {
		return "major"
	}
	if semver.MajorMinor(c) != semver.MajorMinor(l) {
		return "minor"
	}
	return "patch"
}

func stable(version string) bool {
	v := normalizeVersion(version)
	return v != "" && semver.Prerelease(v) == ""
}

func displayVersion(version string, prefix string) string {
	if strings.HasPrefix(prefix, "v") && !strings.HasPrefix(version, "v") {
		return "v" + version
	}
	if !strings.HasPrefix(prefix, "v") && strings.HasPrefix(version, "v") {
		return strings.TrimPrefix(version, "v")
	}
	return version
}

func constraintAllowsLatest(candidate Candidate, latest string) bool {
	if candidate.Manager != ManagerCargo {
		return false
	}
	requirement := strings.TrimSpace(candidate.CurrentValue)
	if requirement == "" || strings.HasPrefix(requirement, "=") || strings.ContainsAny(requirement, "*, <>") {
		return false
	}
	match := constrainedVersion.FindStringSubmatch(requirement)
	if match == nil {
		return false
	}
	current := normalizeVersion(requirement)
	target := normalizeVersion(latest)
	if current == "" || target == "" || semver.Compare(target, current) < 0 {
		return false
	}
	parts := strings.Split(match[1], ".")
	// Cargo prereleases require an explicit prerelease with the same core.
	if semver.Prerelease(target) != "" && (semver.Prerelease(current) == "" ||
		strings.SplitN(semver.Canonical(target), "-", 2)[0] != strings.SplitN(semver.Canonical(current), "-", 2)[0]) {
		return false
	}
	currentMajor := semver.Major(current)
	if strings.HasPrefix(requirement, "~") {
		if len(parts) == 1 {
			return semver.Major(target) == currentMajor
		}
		return semver.MajorMinor(target) == semver.MajorMinor(current)
	}
	if currentMajor != "v0" {
		return semver.Major(target) == currentMajor
	}
	if len(parts) == 1 {
		return semver.Major(target) == "v0"
	}
	if len(parts) == 3 && semver.MajorMinor(current) == "v0.0" {
		return constrainedVersion.FindString(target) == "v"+match[1]
	}
	return semver.MajorMinor(target) == semver.MajorMinor(current)
}
