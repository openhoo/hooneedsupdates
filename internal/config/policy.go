package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// PackageRules are applied cumulatively; conflicting channels or groups block.
type PackageRule struct {
	Dependency    string   `yaml:"dependency" json:"dependency"`
	Managers      []string `yaml:"managers,omitempty" json:"managers,omitempty"`
	Group         string   `yaml:"group,omitempty" json:"group,omitempty"`
	SharedVersion bool     `yaml:"sharedVersion,omitempty" json:"sharedVersion,omitempty"`
	MinimumAge    string   `yaml:"minimumAge,omitempty" json:"minimumAge,omitempty"`
	Channel       string   `yaml:"channel,omitempty" json:"channel,omitempty"`
	MinVersion    string   `yaml:"minVersion,omitempty" json:"minVersion,omitempty"`
	MaxVersion    string   `yaml:"maxVersion,omitempty" json:"maxVersion,omitempty"`
}

func policyVersion(value string) string {
	if value != "" && !strings.HasPrefix(value, "v") {
		return "v" + value
	}
	return value
}
func (c *Config) validatePackageRules() error {
	for i, rule := range c.PackageRules {
		if rule.Dependency == "" {
			return fmt.Errorf("packageRules[%d] requires dependency", i)
		}
		if _, err := regexp.Compile(rule.Dependency); err != nil {
			return fmt.Errorf("packageRules[%d]: %w", i, err)
		}
		if err := validatePolicyManagers("packageRules", rule.Managers); err != nil {
			return err
		}
		if rule.Group != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$`).MatchString(rule.Group) {
			return fmt.Errorf("packageRules[%d] has invalid group", i)
		}
		if rule.SharedVersion && rule.Group == "" {
			return fmt.Errorf("packageRules[%d] sharedVersion requires group", i)
		}
		if rule.Channel != "" && rule.Channel != "stable" && rule.Channel != "prerelease" {
			return fmt.Errorf("packageRules[%d] channel must be stable or prerelease", i)
		}
		if rule.MinimumAge != "" {
			age, err := time.ParseDuration(rule.MinimumAge)
			if err != nil || age < 0 || age > 365*24*time.Hour {
				return fmt.Errorf("packageRules[%d] minimumAge must be between 0s and 8760h", i)
			}
		}
		for _, v := range []string{rule.MinVersion, rule.MaxVersion} {
			if v != "" && !semver.IsValid(policyVersion(v)) {
				return fmt.Errorf("packageRules[%d] invalid version bound %q", i, v)
			}
		}
		if rule.MinVersion != "" && rule.MaxVersion != "" && semver.Compare(policyVersion(rule.MinVersion), policyVersion(rule.MaxVersion)) > 0 {
			return fmt.Errorf("packageRules[%d] version window is empty", i)
		}
	}
	return nil
}
func (c Config) MatchingRules(manager, dependency string) []PackageRule {
	var rules []PackageRule
	for _, rule := range c.PackageRules {
		matched, _ := regexp.MatchString(rule.Dependency, dependency)
		if matched && (len(rule.Managers) == 0 || contains(rule.Managers, manager)) {
			rules = append(rules, rule)
		}
	}
	return rules
}
