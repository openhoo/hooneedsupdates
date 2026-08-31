package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/openhoo/hooneedsupdates/internal/config"
	"github.com/openhoo/hooneedsupdates/internal/update"
)

var version = "dev"
var readBuildInfo = debug.ReadBuildInfo

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printHelp(stdout)
		return 0
	}
	switch args[0] {
	case "scan":
		return runScan(ctx, args[1:], stdout, stderr)
	case "apply":
		return runApply(ctx, args[1:], stdout, stderr)
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "version", "--version", "-version":
		fmt.Fprintf(stdout, "hooneedsupdates %s\n", reportedVersion())
		return 0
	case "help", "--help", "-h":
		printHelp(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printHelp(stderr)
		return 2
	}
}

func reportedVersion() string {
	if version != "dev" {
		return version
	}
	info, ok := readBuildInfo()
	if !ok || info == nil {
		return version
	}
	moduleVersion := strings.TrimPrefix(info.Main.Version, "v")
	if moduleVersion == "" || moduleVersion == "(devel)" {
		return version
	}
	return moduleVersion
}

func runScan(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("scan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "configuration file")
	format := flags.String("format", "table", "table or json")
	failOn := flags.String("fail-on", "never", "never, outdated, or unresolved")
	showAll := flags.Bool("all", false, "include current dependencies in table output")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	root, ok := singleRoot(flags.Args(), stderr)
	if !ok {
		return 2
	}
	report, _, err := scan(ctx, root, *configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch *format {
	case "json":
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	case "table":
		printTable(stdout, report, *showAll)
	default:
		fmt.Fprintf(stderr, "unknown format %q\n", *format)
		return 2
	}
	return reportExit(report, *failOn, stderr)
}

func runApply(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "configuration file")
	write := flags.Bool("write", false, "write the reviewed update plan")
	lockfiles := flags.Bool("lockfiles", false, "regenerate supported lockfiles reproducibly in isolated Git worktrees")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	root, ok := singleRoot(flags.Args(), stderr)
	if !ok {
		return 2
	}
	report, cfg, err := scan(ctx, root, *configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var files []update.AppliedFile
	if *lockfiles {
		lockfileTimeout, parseErr := time.ParseDuration(cfg.LockfileTimeout)
		if parseErr != nil {
			fmt.Fprintf(stderr, "invalid lockfileTimeout: %v\n", parseErr)
			return 1
		}
		files, err = update.ApplyWithLockfiles(ctx, root, report, *write, lockfileTimeout)
	} else {
		files, err = update.Apply(root, report, *write)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No applicable updates.")
		return 0
	}
	mode := "Preview"
	if *write {
		mode = "Applied"
	}
	for _, file := range files {
		detail := fmt.Sprintf("%d updates", file.Updates)
		if file.Kind == "lockfile" {
			detail = "lockfile"
			if file.Created {
				detail = "new lockfile"
			}
		}
		fmt.Fprintf(stdout, "%s %s (%s)\n", mode, file.Path, detail)
	}
	if !*write {
		fmt.Fprintln(stdout, "No files changed. Re-run with --write after review.")
	} else if *lockfiles {
		fmt.Fprintln(stdout, "Manifest and lockfile edits written after two reproducible isolated regenerations.")
	} else {
		fmt.Fprintln(stdout, "Manifest edits written. Re-run with --lockfiles to regenerate supported lockfiles before commit.")
	}
	return 0
}

func runInit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	root, ok := singleRoot(flags.Args(), stderr)
	if !ok {
		return 2
	}
	path := filepath.Join(root, config.FileName)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			fmt.Fprintf(stderr, "%s already exists\n", path)
			return 1
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := io.WriteString(file, starterConfig); err != nil {
		_ = file.Close()
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := file.Close(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Created %s\n", path)
	return 0
}

func scan(ctx context.Context, root, configPath string) (update.Report, config.Config, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return update.Report{}, config.Config{}, err
	}
	cfg, _, err := config.Load(absRoot, configPath)
	if err != nil {
		return update.Report{}, config.Config{}, err
	}
	timeout, err := time.ParseDuration(cfg.RequestTimeout)
	if err != nil {
		return update.Report{}, config.Config{}, fmt.Errorf("invalid requestTimeout: %w", err)
	}
	client := &http.Client{Timeout: timeout}
	report, err := (update.Scanner{Config: cfg, Resolver: update.NewHTTPResolver(client)}).Scan(ctx, absRoot)
	return report, cfg, err
}

func printTable(output io.Writer, report update.Report, all bool) {
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tMANAGER\tDEPENDENCY\tCURRENT\tLATEST\tLOCATION")
	for _, entry := range report.Updates {
		if entry.Status == "current" && !all {
			continue
		}
		latest := entry.LatestVersion
		if entry.Error != "" {
			latest = entry.Error
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s:%d\n",
			entry.Status, entry.Manager, entry.Name, entry.CurrentVersion, latest, entry.File, entry.Line)
	}
	_ = writer.Flush()
	fmt.Fprintf(output, "Detected %d; outdated %d; unresolved %d; ignored %d; current %d.\n",
		report.Summary.Detected, report.Summary.Outdated, report.Summary.Unresolved,
		report.Summary.Ignored, report.Summary.Current)
}

func reportExit(report update.Report, failOn string, stderr io.Writer) int {
	switch failOn {
	case "never":
		return 0
	case "outdated":
		if report.Summary.Outdated > 0 {
			return 2
		}
	case "unresolved":
		if report.Summary.Unresolved > 0 {
			return 3
		}
	default:
		fmt.Fprintf(stderr, "unknown --fail-on value %q\n", failOn)
		return 2
	}
	return 0
}

func singleRoot(args []string, stderr io.Writer) (string, bool) {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "expected at most one repository path")
		return "", false
	}
	if len(args) == 1 {
		return args[0], true
	}
	return ".", true
}

func printHelp(output io.Writer) {
	commands := []string{
		"scan [path]   Resolve and report dependency updates",
		"apply [path]  Preview edits; --lockfiles verifies lockfiles; --write applies them",
		"init [path]   Create strict starter configuration",
		"version       Print version",
	}
	sort.Strings(commands)
	fmt.Fprintln(output, "hooneedsupdates - preview-first dependency update planning")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Usage: hooneedsupdates <command> [options]")
	fmt.Fprintln(output)
	for _, command := range commands {
		fmt.Fprintf(output, "  %s\n", command)
	}
}

var starterConfig = strings.TrimSpace(`
version: 1
managers:
  - gomod
  - cargo
  - npm
  - nuget
  - github-actions
  - docker
excludePaths:
  - '(^|/)(fixtures|testdata|\.oracle)(/|$)'
allowedUpdateTypes:
  - patch
  - minor
  - major
concurrency: 8
requestTimeout: 15s
lockfileTimeout: 5m
includePrereleases: false
customManagers:
  - name: hooversion-version
    datasource: github-releases
    dependencyName: openhoo/hooversion
    filePatterns:
      - '^\\.github/workflows/.*\\.ya?ml$'
    matchStrings:
      - 'HOOVERSION_VERSION:\\s*["'']?(?P<currentValue>[^\\s"'']+)'
`) + "\n"
