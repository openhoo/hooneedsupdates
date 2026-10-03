package update

import (
	"path/filepath"
)

func goGroups(root string, entry Update) ([]*lockfileGroup, error) {
	directory := cleanRelativeDirectory(filepath.Dir(filepath.FromSlash(entry.File)))
	group := newLockfileGroup(ManagerGoMod, directory, "go")
	group.manifests[entry.File] = true
	group.lockfiles[joinRelative(directory, "go.sum")] = true
	if workspace, ok, err := nearestFile(root, directory, "go.work"); err != nil {
		return nil, err
	} else if ok {
		workspaceSum := joinRelative(filepath.Dir(filepath.FromSlash(workspace)), "go.work.sum")
		group.lockfiles[workspaceSum] = true
		group.optional[workspaceSum] = true
	}
	return []*lockfileGroup{group}, nil
}
