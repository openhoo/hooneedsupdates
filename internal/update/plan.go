package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const maxPlanSize = 128 << 20

// Plan captures reviewed output, including generated lockfiles. Applying it
// requires no registry or package-manager invocation.
type Plan struct {
	SchemaVersion int        `json:"schemaVersion"`
	GeneratedAt   time.Time  `json:"generatedAt"`
	Root          string     `json:"root"`
	Report        Report     `json:"report"`
	Lockfiles     bool       `json:"lockfiles"`
	Files         []PlanFile `json:"files"`
	Checksum      string     `json:"checksum"`
}

type PlanFile struct {
	Path         string `json:"path"`
	Mode         uint32 `json:"mode"`
	Existed      bool   `json:"existed"`
	BeforeDigest string `json:"beforeDigest,omitempty"`
	After        []byte `json:"after"`
	Updates      int    `json:"updates"`
	Kind         string `json:"kind"`
}

func contentDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// RequireComplete prevents incomplete inventory from authorizing source writes.
func RequireComplete(report Report) error {
	for _, entry := range report.Updates {
		if entry.Status == "unresolved" || entry.Status == "blocked" || entry.Status == "unsupported" {
			return fmt.Errorf("refusing incomplete plan: %s %s in %s: %s", entry.Status, entry.Name, entry.File, entry.Error)
		}
	}
	if report.Summary.Unresolved > 0 || report.Summary.Blocked > 0 || report.Summary.Unsupported > 0 {
		return fmt.Errorf("refusing incomplete plan: unresolved %d; blocked %d; unsupported %d", report.Summary.Unresolved, report.Summary.Blocked, report.Summary.Unsupported)
	}
	return nil
}

func CreatePlan(root string, report Report, files []AppliedFile, lockfiles bool) (Plan, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Plan{}, err
	}
	if err := RequireComplete(report); err != nil {
		return Plan{}, err
	}
	if err := validateReport(root, report); err != nil {
		return Plan{}, err
	}
	plan := Plan{SchemaVersion: 1, GeneratedAt: time.Now().UTC(), Root: filepath.ToSlash(root), Report: report, Lockfiles: lockfiles, Files: make([]PlanFile, 0, len(files))}
	for _, file := range files {
		path, err := containedPath(root, file.Path)
		if err != nil {
			return Plan{}, err
		}
		mode := os.FileMode(0644)
		if !file.Created {
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				return Plan{}, fmt.Errorf("plan source %s is not a regular file", file.Path)
			}
			mode = info.Mode().Perm()
			current, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(current, file.Before) {
				return Plan{}, fmt.Errorf("plan source %s changed after preview", file.Path)
			}
		}
		saved := PlanFile{Path: file.Path, Mode: uint32(mode), Existed: !file.Created, After: append([]byte(nil), file.After...), Updates: file.Updates, Kind: file.Kind}
		if saved.Existed {
			saved.BeforeDigest = contentDigest(file.Before)
		}
		plan.Files = append(plan.Files, saved)
	}
	sort.Slice(plan.Files, func(i, j int) bool { return plan.Files[i].Path < plan.Files[j].Path })
	plan.Checksum, err = checksumPlan(plan)
	if err != nil {
		return Plan{}, err
	}
	if err := validatePlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func checksumPlan(plan Plan) (string, error) {
	plan.Checksum = ""
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	if len(data) > maxPlanSize {
		return "", errors.New("saved plan exceeds 128 MiB limit")
	}
	return contentDigest(data), nil
}

func validatePlan(plan Plan) error {
	if plan.SchemaVersion != 1 || plan.GeneratedAt.IsZero() || !filepath.IsAbs(filepath.FromSlash(plan.Root)) {
		return errors.New("invalid saved plan metadata")
	}
	checksum, err := checksumPlan(plan)
	if err != nil {
		return err
	}
	if checksum != plan.Checksum {
		return errors.New("saved plan checksum mismatch")
	}
	if err := RequireComplete(plan.Report); err != nil {
		return err
	}
	if plan.Report.Root != plan.Root || plan.Report.SchemaVersion != 2 {
		return errors.New("saved plan report does not match its root or schema")
	}
	if len(plan.Files) == 0 && plan.Report.Summary.Outdated > 0 {
		return errors.New("saved plan is missing approved output")
	}
	seen := map[string]bool{}
	for _, file := range plan.Files {
		if !filepath.IsLocal(filepath.FromSlash(file.Path)) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(file.Path))) != file.Path || seen[file.Path] || file.Mode > 0777 || len(file.After) > maxGeneratedFile || (file.Kind != "manifest" && file.Kind != "lockfile") {
			return fmt.Errorf("invalid saved plan file %q", file.Path)
		}
		seen[file.Path] = true
		if file.Existed && !validImageDigest(file.BeforeDigest) {
			return fmt.Errorf("invalid source digest for %s", file.Path)
		}
		if !file.Existed && file.BeforeDigest != "" {
			return fmt.Errorf("unexpected source digest for new file %s", file.Path)
		}
		if file.Kind == "lockfile" && !plan.Lockfiles {
			return errors.New("manifest-only plan contains lockfile output")
		}
	}
	return nil
}

func SavePlan(path string, plan Plan) error {
	if err := validatePlan(plan); err != nil {
		return err
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	if len(data) >= maxPlanSize {
		return errors.New("saved plan exceeds 128 MiB limit")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	successful := false
	defer func() {
		file.Close()
		if !successful {
			os.Remove(path)
		}
	}()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	successful = true
	return nil
}

func LoadPlan(path string) (Plan, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Plan{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxPlanSize {
		return Plan{}, errors.New("saved plan must be a regular file no larger than 128 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return Plan{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return Plan{}, errors.New("saved plan changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPlanSize+1))
	if err != nil {
		return Plan{}, err
	}
	if len(data) > maxPlanSize {
		return Plan{}, errors.New("saved plan exceeds 128 MiB limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("decode saved plan: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Plan{}, errors.New("saved plan contains trailing data")
	}
	return plan, validatePlan(plan)
}

func ApplyPlan(root string, plan Plan, write bool) ([]AppliedFile, error) {
	if err := validatePlan(plan); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(root) != filepath.Clean(filepath.FromSlash(plan.Root)) {
		return nil, errors.New("saved plan belongs to a different repository root")
	}
	plans := make([]plannedFile, 0, len(plan.Files))
	files := make([]AppliedFile, 0, len(plan.Files))
	for _, saved := range plan.Files {
		path, err := containedPath(root, saved.Path)
		if err != nil {
			return nil, err
		}
		before, existed, mode, err := readOptionalRegular(path, saved.Path)
		if err != nil {
			return nil, err
		}
		if existed != saved.Existed || (existed && (contentDigest(before) != saved.BeforeDigest || uint32(mode.Perm()) != saved.Mode)) {
			return nil, fmt.Errorf("%s changed since the saved plan", saved.Path)
		}
		file := AppliedFile{Path: saved.Path, Updates: saved.Updates, Kind: saved.Kind, Created: !existed, Before: before, After: append([]byte(nil), saved.After...)}
		plans = append(plans, plannedFile{root: root, path: path, mode: os.FileMode(saved.Mode), existed: existed, file: file})
		files = append(files, file)
	}
	if write {
		if err := writePlans(plans); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// WriteDiff emits one contextual unified hunk per text file and summaries for
// binary files. Review and saved-plan apply share the exact same output bytes.
func WriteDiff(output io.Writer, files []AppliedFile) error {
	for _, file := range files {
		if bytes.Equal(file.Before, file.After) {
			continue
		}
		if !utf8.Valid(file.Before) || !utf8.Valid(file.After) || bytes.IndexByte(file.Before, 0) >= 0 || bytes.IndexByte(file.After, 0) >= 0 {
			if _, err := fmt.Fprintf(output, "Binary %s: %s -> %s\n", file.Path, contentDigest(file.Before), contentDigest(file.After)); err != nil {
				return err
			}
			continue
		}
		old, next := diffLines(file.Before), diffLines(file.After)
		prefix := 0
		for prefix < len(old) && prefix < len(next) && old[prefix] == next[prefix] {
			prefix++
		}
		suffix := 0
		for suffix < len(old)-prefix && suffix < len(next)-prefix && old[len(old)-1-suffix] == next[len(next)-1-suffix] {
			suffix++
		}
		start := prefix - 3
		if start < 0 {
			start = 0
		}
		context := suffix
		if context > 3 {
			context = 3
		}
		oldEnd, newEnd := len(old)-suffix+context, len(next)-suffix+context
		oldName := "a/" + file.Path
		if file.Created {
			oldName = "/dev/null"
		}
		oldStart, newStart := start+1, start+1
		if len(old) == 0 {
			oldStart = 0
		}
		if len(next) == 0 {
			newStart = 0
		}
		if _, err := fmt.Fprintf(output, "--- %s\n+++ b/%s\n@@ -%d,%d +%d,%d @@\n", oldName, file.Path, oldStart, oldEnd-start, newStart, newEnd-start); err != nil {
			return err
		}
		emit := func(marker string, line string) error {
			if _, err := io.WriteString(output, marker+line); err != nil {
				return err
			}
			if !strings.HasSuffix(line, "\n") {
				_, err := io.WriteString(output, "\n\\ No newline at end of file\n")
				return err
			}
			return nil
		}
		for i := start; i < prefix; i++ {
			if err := emit(" ", old[i]); err != nil {
				return err
			}
		}
		for i := prefix; i < len(old)-suffix; i++ {
			if err := emit("-", old[i]); err != nil {
				return err
			}
		}
		for i := prefix; i < len(next)-suffix; i++ {
			if err := emit("+", next[i]); err != nil {
				return err
			}
		}
		for i := len(next) - suffix; i < newEnd; i++ {
			if err := emit(" ", next[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

func diffLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
