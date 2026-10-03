package update

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func nugetGroups(root string, entry Update) ([]*lockfileGroup, error) {
	base := filepath.Base(filepath.FromSlash(entry.File))
	var projects []string
	if strings.EqualFold(filepath.Ext(base), ".csproj") {
		projects = []string{entry.File}
	} else if base == "Directory.Packages.props" {
		var err error
		projects, err = nugetProjects(root, cleanRelativeDirectory(filepath.Dir(filepath.FromSlash(entry.File))))
		if err != nil {
			return nil, err
		}
		if len(projects) == 0 {
			return nil, fmt.Errorf("%s has no descendant .csproj files", entry.File)
		}
	} else {
		return nil, fmt.Errorf("unsupported NuGet manifest %s", entry.File)
	}
	groups := make([]*lockfileGroup, 0, len(projects))
	for _, project := range projects {
		directory := cleanRelativeDirectory(filepath.Dir(filepath.FromSlash(project)))
		group := newLockfileGroup(ManagerNuGet, directory, "dotnet")
		group.manifests[entry.File] = true
		group.projects[project] = true
		group.lockfiles[joinRelative(directory, "packages.lock.json")] = true
		groups = append(groups, group)
	}
	return groups, nil
}

func nugetProjects(root, directory string) ([]string, error) {
	start, err := containedPath(root, joinRelative(directory, "."))
	if err != nil {
		return nil, err
	}
	var projects []string
	err = filepath.WalkDir(start, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != start && excludedDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".csproj") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		projects = append(projects, filepath.ToSlash(relative))
		return nil
	})
	sort.Strings(projects)
	return projects, err
}
