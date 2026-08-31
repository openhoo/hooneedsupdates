// Command sync_release_version keeps release examples aligned with VERSION.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var stableVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func main() {
	if err := syncReleaseVersion("."); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func syncReleaseVersion(root string) error {
	raw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return fmt.Errorf("read VERSION: %w", err)
	}
	version := strings.TrimSpace(string(raw))
	if !stableVersion.MatchString(version) {
		return fmt.Errorf("VERSION must contain one stable semantic version, got %q", version)
	}

	readmePath := filepath.Join(root, "README.md")
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		return fmt.Errorf("read README.md: %w", err)
	}
	updated := string(readme)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(@v)[0-9]+\.[0-9]+\.[0-9]+`),
		regexp.MustCompile(`(hooneedsupdates:v)[0-9]+\.[0-9]+\.[0-9]+`),
		regexp.MustCompile(`(# v)[0-9]+\.[0-9]+\.[0-9]+`),
		regexp.MustCompile(`(?m)^(    version: )[0-9]+\.[0-9]+\.[0-9]+(\s*)$`),
	}
	for _, pattern := range patterns {
		if !pattern.MatchString(updated) {
			return fmt.Errorf("README.md: expected release marker %q", pattern.String())
		}
		updated = pattern.ReplaceAllString(updated, "${1}"+version+"${2}")
	}
	if updated == string(readme) {
		return nil
	}
	if err := os.WriteFile(readmePath, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write README.md: %w", err)
	}
	return nil
}
