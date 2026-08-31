package update

import (
	"bytes"
	"fmt"
	"os"
	"sort"
)

type pinnedCargoFile struct {
	path     string
	relative string
	mode     os.FileMode
	approved []byte
	pinned   []byte
}

func pinCargoManifests(worktree string, plan regenerationPlan) ([]pinnedCargoFile, error) {
	updatesByFile := map[string][]Update{}
	for _, group := range plan.groups {
		if group.manager != ManagerCargo {
			continue
		}
		for _, entry := range group.updates {
			updatesByFile[entry.File] = append(updatesByFile[entry.File], entry)
		}
	}
	files := make([]string, 0, len(updatesByFile))
	for file := range updatesByFile {
		files = append(files, file)
	}
	sort.Strings(files)
	result := make([]pinnedCargoFile, 0, len(files))
	for _, relative := range files {
		path, err := containedPath(worktree, relative)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		approved, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		candidates := extractCargo(relative, approved)
		var edits []edit
		seen := map[string]bool{}
		for _, entry := range updatesByFile[relative] {
			key := entry.Name + "\x00" + fmt.Sprintf("%d", entry.Line)
			if seen[key] {
				continue
			}
			seen[key] = true
			matched := false
			for _, candidate := range candidates {
				if candidate.Name != entry.Name || candidate.Line != entry.Line {
					continue
				}
				if normalizeVersion(candidate.CurrentVersion) != normalizeVersion(entry.LatestVersion) {
					return nil, fmt.Errorf("approved Cargo version for %s in %s:%d is %s, expected %s", entry.Name, relative, entry.Line, candidate.CurrentVersion, entry.LatestVersion)
				}
				edits = append(edits, edit{
					start: candidate.Start, end: candidate.End, oldValue: candidate.CurrentValue,
					replacement: "=" + candidate.CurrentValue,
				})
				matched = true
				break
			}
			if !matched {
				return nil, fmt.Errorf("updated Cargo dependency %s not found in %s:%d", entry.Name, relative, entry.Line)
			}
		}
		sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
		pinned, err := applyEdits(approved, relative, edits)
		if err != nil {
			return nil, err
		}
		result = append(result, pinnedCargoFile{
			path: path, relative: relative, mode: info.Mode().Perm(), approved: approved, pinned: pinned,
		})
	}
	for _, file := range result {
		if err := atomicWrite(file.path, file.pinned, file.mode); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func restoreCargoManifests(files []pinnedCargoFile) error {
	for _, file := range files {
		current, err := os.ReadFile(file.path)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, file.pinned) {
			return fmt.Errorf("Cargo changed temporarily pinned manifest %s", file.relative)
		}
	}
	for _, file := range files {
		if err := atomicWrite(file.path, file.approved, file.mode); err != nil {
			return err
		}
	}
	return nil
}
