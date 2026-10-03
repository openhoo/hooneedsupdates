package update

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cargoGroups(root string, entry Update) ([]*lockfileGroup, error) {
	directory, err := cargoWorkspace(root, entry.File)
	if err != nil {
		return nil, err
	}
	group := newLockfileGroup(ManagerCargo, directory, "cargo")
	group.manifests[entry.File] = true
	group.lockfiles[joinRelative(directory, "Cargo.lock")] = true
	return []*lockfileGroup{group}, nil
}

func cargoWorkspace(root, manifest string) (string, error) {
	start := cleanRelativeDirectory(filepath.Dir(filepath.FromSlash(manifest)))
	if lockfile, ok, err := nearestFile(root, start, "Cargo.lock"); err != nil {
		return "", err
	} else if ok {
		return cleanRelativeDirectory(filepath.Dir(filepath.FromSlash(lockfile))), nil
	}
	root = filepath.Clean(root)
	directory := filepath.Join(root, filepath.FromSlash(start))
	for {
		manifestPath := filepath.Join(directory, "Cargo.toml")
		data, err := os.ReadFile(manifestPath)
		if err == nil && hasCargoWorkspace(data) {
			relative, err := filepath.Rel(root, directory)
			if err != nil {
				return "", err
			}
			return cleanRelativeDirectory(relative), nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if directory == root {
			break
		}
		directory = filepath.Dir(directory)
	}
	return start, nil
}

func hasCargoWorkspace(data []byte) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "[workspace]" || strings.HasPrefix(trimmed, "[workspace] #") {
			return true
		}
	}
	return false
}

func rejectCargoProjectConfig(root string, groups []*lockfileGroup) error {
	checked := map[string]bool{}
	for _, group := range groups {
		if group.manager != ManagerCargo {
			continue
		}
		directory := filepath.Join(root, filepath.FromSlash(group.directory))
		for {
			for _, name := range []string{"config.toml", "config"} {
				candidate := filepath.Join(directory, ".cargo", name)
				if checked[candidate] {
					continue
				}
				checked[candidate] = true
				if _, err := os.Lstat(candidate); err == nil {
					return fmt.Errorf("repository Cargo configuration is not allowed in lockfile mode: %s", candidate)
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			if filepath.Clean(directory) == filepath.Clean(root) {
				break
			}
			parent := filepath.Dir(directory)
			if parent == directory || !pathWithin(root, parent) {
				break
			}
			directory = parent
		}
	}
	return nil
}
