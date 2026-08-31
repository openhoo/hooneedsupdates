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
	IncludePrereleases bool            `yaml:"includePrereleases,omitempty"`
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
	}
	for _, validate := range validators {
		if err := validate(); err != nil {
			return err
		}
	}
	return nil
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
