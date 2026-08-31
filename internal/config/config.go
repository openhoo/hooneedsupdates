package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const FileName = "hooneedsupdates.yaml"

var knownManagers = map[string]bool{
	"gomod": true, "cargo": true, "npm": true, "nuget": true,
	"github-actions": true, "docker": true,
}

type Config struct {
	Version            int             `yaml:"version"`
	Managers           []string        `yaml:"managers,omitempty"`
	ExcludePaths       []string        `yaml:"excludePaths,omitempty"`
	Ignore             []IgnoreRule    `yaml:"ignore,omitempty"`
	CustomManagers     []CustomManager `yaml:"customManagers,omitempty"`
	AllowedUpdateTypes []string        `yaml:"allowedUpdateTypes,omitempty"`
	Concurrency        int             `yaml:"concurrency,omitempty"`
	RequestTimeout     string          `yaml:"requestTimeout,omitempty"`
	LockfileTimeout    string          `yaml:"lockfileTimeout,omitempty"`
	IncludePrereleases bool            `yaml:"includePrereleases,omitempty"`
	Automation         Automation      `yaml:"automation,omitempty"`
}

type Automation struct {
	Repositories []string  `yaml:"repositories,omitempty"`
	BranchPrefix string    `yaml:"branchPrefix,omitempty"`
	Labels       []string  `yaml:"labels,omitempty"`
	Lockfiles    bool      `yaml:"lockfiles,omitempty"`
	Draft        bool      `yaml:"draft,omitempty"`
	Selection    Selection `yaml:"selection,omitempty"`
	AutoMerge    AutoMerge `yaml:"autoMerge,omitempty"`
	MergeMethod  string    `yaml:"mergeMethod,omitempty"`
	CloseStale   bool      `yaml:"closeStale,omitempty"`
	CommitAuthor string    `yaml:"commitAuthor,omitempty"`
	CommitEmail  string    `yaml:"commitEmail,omitempty"`
}

type Selection struct {
	UpdateTypes  []string `yaml:"updateTypes,omitempty"`
	Managers     []string `yaml:"managers,omitempty"`
	Dependencies []string `yaml:"dependencies,omitempty"`
}

type AutoMerge struct {
	Enabled          bool     `yaml:"enabled,omitempty"`
	UpdateTypes      []string `yaml:"updateTypes,omitempty"`
	Managers         []string `yaml:"managers,omitempty"`
	Dependencies     []string `yaml:"dependencies,omitempty"`
	MaxUpdates       int      `yaml:"maxUpdates,omitempty"`
	RequireLockfiles bool     `yaml:"requireLockfiles,omitempty"`
}

type IgnoreRule struct {
	Dependency string   `yaml:"dependency"`
	Managers   []string `yaml:"managers,omitempty"`
	Reason     string   `yaml:"reason"`
}

type CustomManager struct {
	Name           string   `yaml:"name"`
	Datasource     string   `yaml:"datasource"`
	DependencyName string   `yaml:"dependencyName"`
	FilePatterns   []string `yaml:"filePatterns"`
	MatchStrings   []string `yaml:"matchStrings"`
}

func Default() Config {
	return Config{
		Version:            1,
		Managers:           []string{"gomod", "cargo", "npm", "nuget", "github-actions", "docker"},
		ExcludePaths:       []string{`(^|/)(fixtures|testdata|\.oracle)(/|$)`},
		AllowedUpdateTypes: []string{"patch", "minor", "major"},
		Concurrency:        8,
		RequestTimeout:     "15s",
		LockfileTimeout:    "5m",
		Automation: Automation{
			BranchPrefix: "hooneedsupdates",
			Lockfiles:    true,
			AutoMerge: AutoMerge{
				UpdateTypes:      []string{"patch", "minor"},
				MaxUpdates:       20,
				RequireLockfiles: true,
			},
			MergeMethod:  "squash",
			CloseStale:   true,
			CommitAuthor: "HooNeedsUpdates Bot",
			CommitEmail:  "hooneedsupdates[bot]@users.noreply.github.com",
		},
	}
}

func Load(root, explicit string) (Config, string, error) {
	cfg := Default()
	path, err := resolvePath(root, explicit)
	if err != nil {
		return Config{}, "", err
	}
	if path == "" {
		return cfg, "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, "", err
	}
	var header struct {
		Version *int `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return Config{}, "", fmt.Errorf("parse %s: %w", path, err)
	}
	if header.Version == nil {
		return Config{}, "", fmt.Errorf("validate %s: version is required", path)
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, "", fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, "", fmt.Errorf("validate %s: %w", path, err)
	}
	return cfg, path, nil
}

func resolvePath(root, explicit string) (string, error) {
	if explicit != "" {
		if filepath.IsAbs(explicit) {
			return explicit, nil
		}
		return filepath.Join(root, explicit), nil
	}
	candidate := filepath.Join(root, FileName)
	_, err := os.Stat(candidate)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return candidate, nil
}

func (c *Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported version %d", c.Version)
	}
	validators := []func() error{
		c.validateManagers,
		c.validateExcludePaths,
		c.validateRuntime,
		c.validateUpdateTypes,
		c.validateIgnoreRules,
		c.validateCustomManagers,
		c.validateAutomation,
	}
	for _, validate := range validators {
		if err := validate(); err != nil {
			return err
		}
	}
	return nil
}

var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (c *Config) validateAutomation() error {
	automation := c.Automation
	validators := []func() error{
		func() error { return validateAutomationRepositories(automation.Repositories) },
		func() error { return validateBranchPrefix(automation.BranchPrefix) },
		func() error { return validateAutomationLabels(automation.Labels) },
		func() error { return validateMergeMethod(automation.MergeMethod) },
		func() error { return validateAutoMerge(automation) },
		func() error { return validateSelection(automation.Selection) },
		func() error { return validateAutomationIdentity(automation) },
	}
	for _, validate := range validators {
		if err := validate(); err != nil {
			return err
		}
	}
	if automation.AutoMerge.Enabled && automation.Draft {
		return errors.New("automation.autoMerge cannot be enabled for draft pull requests")
	}
	return nil
}

func validateAutomationRepositories(repositories []string) error {
	if len(repositories) > 100 {
		return errors.New("automation.repositories must not contain more than 100 repositories")
	}
	seenRepositories := map[string]bool{}
	for index, repository := range repositories {
		if !repositoryName.MatchString(repository) || strings.HasSuffix(strings.ToLower(repository), ".git") {
			return fmt.Errorf("automation.repositories[%d] must be owner/repository", index)
		}
		normalized := strings.ToLower(repository)
		if seenRepositories[normalized] {
			return fmt.Errorf("duplicate automation repository %q", repository)
		}
		seenRepositories[normalized] = true
	}
	return nil
}

func validateAutomationLabels(labels []string) error {
	if len(labels) > 20 {
		return errors.New("automation.labels must not contain more than 20 labels")
	}
	seenLabels := map[string]bool{}
	for index, label := range labels {
		trimmed := strings.TrimSpace(label)
		if label != trimmed || trimmed == "" || len(label) > 50 || strings.ContainsAny(label, "\r\n") {
			return fmt.Errorf("automation.labels[%d] must be a non-empty single-line label no longer than 50 characters", index)
		}
		if seenLabels[label] {
			return fmt.Errorf("duplicate automation label %q", label)
		}
		seenLabels[label] = true
	}
	return nil
}

func validateMergeMethod(method string) error {
	switch method {
	case "merge", "squash", "rebase":
		return nil
	default:
		return fmt.Errorf("automation.mergeMethod must be merge, squash, or rebase, got %q", method)
	}
}

func validateAutomationIdentity(automation Automation) error {
	if strings.TrimSpace(automation.CommitAuthor) == "" || strings.ContainsAny(automation.CommitAuthor, "\r\n") {
		return errors.New("automation.commitAuthor must be a non-empty single-line value")
	}
	if strings.TrimSpace(automation.CommitEmail) == "" || strings.ContainsAny(automation.CommitEmail, "\r\n<>") || !strings.Contains(automation.CommitEmail, "@") {
		return errors.New("automation.commitEmail must be a valid single-line email address")
	}
	return nil
}

func validateSelection(selection Selection) error {
	if err := validatePolicyUpdateTypes("automation.selection", selection.UpdateTypes); err != nil {
		return err
	}
	if err := validatePolicyManagers("automation.selection", selection.Managers); err != nil {
		return err
	}
	return validatePolicyPatterns("automation.selection.dependencies", selection.Dependencies)
}

func validateAutoMerge(automation Automation) error {
	policy := automation.AutoMerge
	if len(policy.UpdateTypes) == 0 {
		return errors.New("automation.autoMerge.updateTypes must not be empty")
	}
	if err := validatePolicyUpdateTypes("automation.autoMerge", policy.UpdateTypes); err != nil {
		return err
	}
	if err := validatePolicyManagers("automation.autoMerge", policy.Managers); err != nil {
		return err
	}
	if err := validatePolicyPatterns("automation.autoMerge.dependencies", policy.Dependencies); err != nil {
		return err
	}
	if policy.MaxUpdates < 1 || policy.MaxUpdates > 1000 {
		return errors.New("automation.autoMerge.maxUpdates must be between 1 and 1000")
	}
	if policy.Enabled && policy.RequireLockfiles && !automation.Lockfiles {
		return errors.New("automation.autoMerge requires automation.lockfiles when requireLockfiles is true")
	}
	return nil
}

func validatePolicyUpdateTypes(field string, values []string) error {
	seenUpdateTypes := map[string]bool{}
	for _, updateType := range values {
		if updateType != "patch" && updateType != "minor" && updateType != "major" {
			return fmt.Errorf("%s has unknown update type %q", field, updateType)
		}
		if seenUpdateTypes[updateType] {
			return fmt.Errorf("%s has duplicate update type %q", field, updateType)
		}
		seenUpdateTypes[updateType] = true
	}
	return nil
}

func validatePolicyManagers(field string, values []string) error {
	seenManagers := map[string]bool{}
	for _, manager := range values {
		if !knownManagers[manager] && manager != "custom" {
			return fmt.Errorf("%s has unknown manager %q", field, manager)
		}
		if seenManagers[manager] {
			return fmt.Errorf("%s has duplicate manager %q", field, manager)
		}
		seenManagers[manager] = true
	}
	return nil
}

func validatePolicyPatterns(field string, values []string) error {
	for index, pattern := range values {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("%s[%d]: %w", field, index, err)
		}
	}
	return nil
}

func validateBranchPrefix(prefix string) error {
	if unsafeBranchPrefix(prefix) {
		return fmt.Errorf("automation.branchPrefix %q is not a safe Git ref prefix", prefix)
	}
	for _, component := range strings.Split(prefix, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") ||
			strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("automation.branchPrefix %q is not a safe Git ref prefix", prefix)
		}
	}
	return nil
}

func unsafeBranchPrefix(prefix string) bool {
	if prefix == "" || len(prefix) > 180 {
		return true
	}
	if strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") ||
		strings.HasPrefix(prefix, ".") || strings.HasSuffix(prefix, ".") {
		return true
	}
	for _, character := range prefix {
		if character <= 0x20 || character == 0x7f {
			return true
		}
	}
	return strings.Contains(prefix, "..") || strings.Contains(prefix, "@{") ||
		strings.Contains(prefix, "//") || strings.ContainsAny(prefix, " ~^:?*[\\\r\n")
}

func (c *Config) validateManagers() error {
	seenManagers := map[string]bool{}
	if len(c.Managers) == 0 {
		return errors.New("managers must not be empty")
	}
	for _, manager := range c.Managers {
		if !knownManagers[manager] {
			return fmt.Errorf("unknown manager %q", manager)
		}
		if seenManagers[manager] {
			return fmt.Errorf("duplicate manager %q", manager)
		}
		seenManagers[manager] = true
	}
	return nil
}

func (c *Config) validateExcludePaths() error {
	for index, pattern := range c.ExcludePaths {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("excludePaths[%d]: %w", index, err)
		}
	}
	return nil
}

func (c *Config) validateRuntime() error {
	if c.Concurrency < 1 || c.Concurrency > 32 {
		return errors.New("concurrency must be between 1 and 32")
	}
	requestTimeout, err := time.ParseDuration(c.RequestTimeout)
	if err != nil || requestTimeout <= 0 {
		return errors.New("requestTimeout must be a positive duration")
	}
	lockfileTimeout, err := time.ParseDuration(c.LockfileTimeout)
	if err != nil || lockfileTimeout <= 0 || lockfileTimeout > 30*time.Minute {
		return errors.New("lockfileTimeout must be a positive duration no greater than 30m")
	}
	return nil
}

func (c *Config) validateUpdateTypes() error {
	allowed := map[string]bool{"patch": true, "minor": true, "major": true}
	for _, kind := range c.AllowedUpdateTypes {
		if !allowed[kind] {
			return fmt.Errorf("unknown allowed update type %q", kind)
		}
	}
	return nil
}

func (c *Config) validateIgnoreRules() error {
	for index, rule := range c.Ignore {
		if strings.TrimSpace(rule.Dependency) == "" || strings.TrimSpace(rule.Reason) == "" {
			return fmt.Errorf("ignore[%d] requires dependency and reason", index)
		}
		if _, err := regexp.Compile(rule.Dependency); err != nil {
			return fmt.Errorf("ignore[%d].dependency: %w", index, err)
		}
		for _, manager := range rule.Managers {
			if !knownManagers[manager] && manager != "custom" {
				return fmt.Errorf("ignore[%d] has unknown manager %q", index, manager)
			}
		}
	}
	return nil
}

func (c *Config) validateCustomManagers() error {
	seen := map[string]bool{}
	for index, manager := range c.CustomManagers {
		if err := validateCustomManager(index, manager, seen); err != nil {
			return err
		}
	}
	return nil
}

func validateCustomManager(index int, manager CustomManager, seen map[string]bool) error {
	if manager.Name == "" || manager.DependencyName == "" || manager.Datasource == "" {
		return fmt.Errorf("customManagers[%d] requires name, datasource, and dependencyName", index)
	}
	if strings.Count(manager.DependencyName, "/") != 1 {
		return fmt.Errorf("custom manager %q dependencyName must be owner/repository", manager.Name)
	}
	if manager.Datasource != "github-releases" {
		return fmt.Errorf("customManagers[%d] uses unsupported datasource %q", index, manager.Datasource)
	}
	if seen[manager.Name] {
		return fmt.Errorf("duplicate custom manager %q", manager.Name)
	}
	seen[manager.Name] = true
	if len(manager.FilePatterns) == 0 || len(manager.MatchStrings) == 0 {
		return fmt.Errorf("custom manager %q needs filePatterns and matchStrings", manager.Name)
	}
	if err := validatePatterns(manager.Name, manager.FilePatterns, false); err != nil {
		return err
	}
	return validatePatterns(manager.Name, manager.MatchStrings, true)
}

func validatePatterns(name string, patterns []string, requireCapture bool) error {
	for _, pattern := range patterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("custom manager %q pattern: %w", name, err)
		}
		if requireCapture && compiled.SubexpIndex("currentValue") < 0 {
			return fmt.Errorf("custom manager %q match string lacks (?P<currentValue>...)", name)
		}
	}
	return nil
}

func (c Config) PathExcluded(path string) bool {
	for _, pattern := range c.ExcludePaths {
		compiled, err := regexp.Compile(pattern)
		if err == nil && compiled.MatchString(path) {
			return true
		}
	}
	return false
}

func (c Config) ManagerEnabled(manager string) bool {
	return contains(c.Managers, manager)
}

func (c Config) UpdateTypeAllowed(kind string) bool {
	return contains(c.AllowedUpdateTypes, kind)
}

func (c Config) IgnoreReason(manager, dependency string) string {
	for _, rule := range c.Ignore {
		matched, _ := regexp.MatchString(rule.Dependency, dependency)
		if !matched || (len(rule.Managers) > 0 && !contains(rule.Managers, manager)) {
			continue
		}
		return rule.Reason
	}
	return ""
}

func (c Config) ManagerNames() []string {
	names := append([]string{}, c.Managers...)
	for _, custom := range c.CustomManagers {
		names = append(names, "custom:"+custom.Name)
	}
	sort.Strings(names)
	return names
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
