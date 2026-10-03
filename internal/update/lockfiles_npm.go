package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func npmGroups(root string, entry Update) ([]*lockfileGroup, error) {
	directory, tool, lockfile, err := npmLockfile(root, entry.File)
	if err != nil {
		return nil, err
	}
	group := newLockfileGroup(ManagerNPM, directory, tool)
	group.manifests[entry.File] = true
	group.lockfiles[lockfile] = true
	return []*lockfileGroup{group}, nil
}

func npmLockfile(root, manifest string) (directory, tool, lockfile string, err error) {
	start := cleanRelativeDirectory(filepath.Dir(filepath.FromSlash(manifest)))
	root = filepath.Clean(root)
	current := filepath.Join(root, filepath.FromSlash(start))
	for {
		var matches []struct {
			name string
			tool string
		}
		for _, candidate := range []struct {
			name string
			tool string
		}{{"bun.lock", "bun"}, {"bun.lockb", "bun"}, {"package-lock.json", "npm"}} {
			info, statErr := os.Lstat(filepath.Join(current, candidate.name))
			if statErr == nil {
				if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
					return "", "", "", fmt.Errorf("refusing non-regular %s", filepath.Join(current, candidate.name))
				}
				matches = append(matches, candidate)
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return "", "", "", statErr
			}
		}
		if len(matches) > 1 {
			return "", "", "", fmt.Errorf("multiple JavaScript lockfiles beside or above %s", manifest)
		}
		if len(matches) == 1 {
			relativeDirectory, relErr := filepath.Rel(root, current)
			if relErr != nil {
				return "", "", "", relErr
			}
			directory = cleanRelativeDirectory(relativeDirectory)
			return directory, matches[0].tool, joinRelative(directory, matches[0].name), nil
		}
		if current == root {
			break
		}
		current = filepath.Dir(current)
	}
	data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(manifest)))
	if readErr != nil {
		return "", "", "", readErr
	}
	var metadata struct {
		PackageManager string `json:"packageManager"`
	}
	if unmarshalErr := json.Unmarshal(data, &metadata); unmarshalErr != nil {
		return "", "", "", unmarshalErr
	}
	directory = start
	switch {
	case strings.HasPrefix(metadata.PackageManager, "bun@"):
		return directory, "bun", joinRelative(directory, "bun.lock"), nil
	case strings.HasPrefix(metadata.PackageManager, "npm@"):
		return directory, "npm", joinRelative(directory, "package-lock.json"), nil
	default:
		return "", "", "", fmt.Errorf("%s has no supported lockfile or bun/npm packageManager", manifest)
	}
}
