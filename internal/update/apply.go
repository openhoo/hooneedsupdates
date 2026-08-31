package update

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type AppliedFile struct {
	Path    string `json:"path"`
	Updates int    `json:"updates"`
	Before  []byte `json:"-"`
	After   []byte `json:"-"`
}

type edit struct {
	start       int
	end         int
	oldValue    string
	replacement string
}

type plannedFile struct {
	path string
	mode os.FileMode
	file AppliedFile
}

func Apply(root string, report Report, write bool) ([]AppliedFile, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	plans, err := planReport(absRoot, report)
	if err != nil {
		return nil, err
	}
	result := make([]AppliedFile, 0, len(plans))
	for _, plan := range plans {
		result = append(result, plan.file)
	}
	if write {
		if err := writePlans(plans); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func planReport(root string, report Report) ([]plannedFile, error) {
	byFile := map[string][]Update{}
	for _, entry := range report.Updates {
		if entry.Status == "outdated" {
			byFile[entry.File] = append(byFile[entry.File], entry)
		}
	}
	files := make([]string, 0, len(byFile))
	for file := range byFile {
		files = append(files, file)
	}
	sort.Strings(files)
	var plans []plannedFile
	for _, rel := range files {
		plan, err := planUpdates(root, rel, byFile[rel])
		if err != nil {
			return nil, err
		}
		if plan != nil {
			plans = append(plans, *plan)
		}
	}
	return plans, nil
}

func writePlans(plans []plannedFile) error {
	for index, plan := range plans {
		if err := atomicWrite(plan.path, plan.file.After, plan.mode); err != nil {
			rollbackErr := rollback(plans[:index+1])
			if rollbackErr != nil {
				return fmt.Errorf("write %s: %w; rollback also failed: %v", plan.file.Path, err, rollbackErr)
			}
			return fmt.Errorf("write %s: %w; earlier writes rolled back", plan.file.Path, err)
		}
	}
	return nil
}

func planUpdates(root, rel string, updates []Update) (*plannedFile, error) {
	path, err := containedPath(root, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing to edit non-regular file %s", rel)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	afterReadInfo, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, afterReadInfo) {
		return nil, fmt.Errorf("%s changed while it was read", rel)
	}
	edits, err := buildEdits(before, rel, updates)
	if err != nil {
		return nil, err
	}
	after, err := applyEdits(before, rel, edits)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(before, after) {
		return nil, nil
	}
	file := AppliedFile{Path: rel, Updates: len(updates), Before: before, After: after}
	return &plannedFile{path: path, mode: info.Mode().Perm(), file: file}, nil
}

func buildEdits(before []byte, rel string, updates []Update) ([]edit, error) {
	edits := make([]edit, 0, len(updates)*2)
	for _, entry := range updates {
		replacement := replacementFor(entry)
		if replacement == "" {
			return nil, fmt.Errorf("no safe replacement for %s in %s:%d", entry.Name, rel, entry.Line)
		}
		edits = append(edits, edit{start: entry.Start, end: entry.End, oldValue: entry.CurrentValue, replacement: replacement})
		if entry.Manager == ManagerGitHubActions {
			if commentEdit, ok := actionCommentEdit(before, entry); ok {
				edits = append(edits, commentEdit)
			}
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	return edits, nil
}

func applyEdits(before []byte, rel string, edits []edit) ([]byte, error) {
	after := append([]byte(nil), before...)
	lastStart := len(after) + 1
	for _, planned := range edits {
		if planned.start < 0 || planned.end > len(after) || planned.start > planned.end {
			return nil, fmt.Errorf("invalid edit range for %s", rel)
		}
		if planned.end > lastStart {
			return nil, fmt.Errorf("overlapping edits for %s", rel)
		}
		if string(after[planned.start:planned.end]) != planned.oldValue {
			return nil, fmt.Errorf("%s changed after scan at byte %d", rel, planned.start)
		}
		after = append(after[:planned.start], append([]byte(planned.replacement), after[planned.end:]...)...)
		lastStart = planned.start
	}
	return after, nil
}

func containedPath(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid repository-relative path %q", rel)
	}
	path := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes repository root: %q", rel)
	}
	return path, nil
}

func rollback(plans []plannedFile) error {
	var failures []string
	for index := len(plans) - 1; index >= 0; index-- {
		plan := plans[index]
		if err := atomicWrite(plan.path, plan.file.Before, plan.mode); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", plan.file.Path, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func replacementFor(entry Update) string {
	if entry.Manager == ManagerGitHubActions {
		return entry.LatestDigest
	}
	latest := entry.LatestVersion
	if latest == "" {
		return ""
	}
	latest = displayVersion(latest, entry.CurrentVersion)
	return entry.Prefix + latest + entry.Suffix
}

func actionCommentEdit(data []byte, entry Update) (edit, bool) {
	lineStart := bytes.LastIndex(data[:entry.Start], []byte("\n")) + 1
	lineEndOffset := bytes.IndexByte(data[entry.End:], '\n')
	lineEnd := len(data)
	if lineEndOffset >= 0 {
		lineEnd = entry.End + lineEndOffset
	}
	line := data[lineStart:lineEnd]
	marker := bytes.IndexByte(line, '#')
	if marker < 0 {
		insertAt := lineEnd
		if insertAt > lineStart && data[insertAt-1] == '\r' {
			insertAt--
		}
		return edit{start: insertAt, end: insertAt, oldValue: "", replacement: " # " + entry.LatestVersion}, true
	}
	commentStart := lineStart + marker + 1
	for commentStart < lineEnd && (data[commentStart] == ' ' || data[commentStart] == '\t') {
		commentStart++
	}
	commentEnd := commentStart
	for commentEnd < lineEnd && data[commentEnd] != ' ' && data[commentEnd] != '\t' && data[commentEnd] != '\r' {
		commentEnd++
	}
	old := string(data[commentStart:commentEnd])
	if normalizeVersion(old) == "" || normalizeVersion(old) != normalizeVersion(entry.CurrentVersion) {
		return edit{}, false
	}
	latest := displayVersion(entry.LatestVersion, old)
	return edit{start: commentStart, end: commentEnd, oldValue: old, replacement: latest}, true
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".hooneedsupdates-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryPath) }
	defer cleanup()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return directoryHandle.Sync()
}
