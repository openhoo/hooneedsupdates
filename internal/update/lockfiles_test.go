package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeLockRunner struct {
	mu    sync.Mutex
	calls int
	run   func(int, lockCommand) error
}

func (runner *fakeLockRunner) Run(_ context.Context, command lockCommand) ([]byte, error) {
	runner.mu.Lock()
	runner.calls++
	call := runner.calls
	runner.mu.Unlock()
	if runner.run != nil {
		return nil, runner.run(call, command)
	}
	return nil, nil
}

func TestApplyWithLockfilesPreviewAndWriteAreReproducible(t *testing.T) {
	root := gitFixture(t, map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.26\n\nrequire example.com/dependency v1.0.0\n",
		"go.sum": "before\n",
	})
	report := fixtureReport(t, root, "go.mod", ManagerGoMod, "example.com/dependency", "v1.0.0", "v1.1.0")
	runner := &fakeLockRunner{run: func(_ int, command lockCommand) error {
		if command.manager != ManagerGoMod || command.name != "go" || strings.Join(command.args, " ") != "mod tidy" {
			return fmt.Errorf("unexpected command: %#v", command)
		}
		return os.WriteFile(filepath.Join(command.dir, "go.sum"), []byte("reproducible\n"), 0o644)
	}}

	preview, err := applyWithLockfiles(context.Background(), root, report, false, time.Second, runner)
	if err != nil {
		t.Fatal(err)
	}
	assertAppliedPaths(t, preview, "go.mod", "go.sum")
	assertFile(t, root, "go.mod", "v1.0.0")
	assertFile(t, root, "go.sum", "before\n")

	runner.calls = 0
	applied, err := applyWithLockfiles(context.Background(), root, report, true, time.Second, runner)
	if err != nil {
		t.Fatal(err)
	}
	assertAppliedPaths(t, applied, "go.mod", "go.sum")
	assertFile(t, root, "go.mod", "v1.1.0")
	assertFile(t, root, "go.sum", "reproducible\n")
}

func TestApplyWithLockfilesRejectsUnexpectedAndNonReproducibleOutput(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(int, lockCommand) error
		want string
	}{
		{
			name: "unexpected file",
			run: func(_ int, command lockCommand) error {
				if err := os.WriteFile(filepath.Join(command.dir, "go.sum"), []byte("stable\n"), 0o644); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(command.dir, "unexpected.txt"), []byte("no\n"), 0o644)
			},
			want: "unexpected path unexpected.txt",
		},
		{
			name: "manifest mutation",
			run: func(_ int, command lockCommand) error {
				if err := os.WriteFile(filepath.Join(command.dir, "go.sum"), []byte("stable\n"), 0o644); err != nil {
					return err
				}
				file, err := os.OpenFile(filepath.Join(command.dir, "go.mod"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					return err
				}
				defer file.Close()
				_, err = file.WriteString("// changed\n")
				return err
			},
			want: "changed approved manifest go.mod",
		},
		{
			name: "different second run",
			run: func(call int, command lockCommand) error {
				return os.WriteFile(filepath.Join(command.dir, "go.sum"), []byte(fmt.Sprintf("run-%d\n", call)), 0o644)
			},
			want: "not byte-reproducible for go.sum",
		},
		{
			name: "tool failure",
			run:  func(_ int, _ lockCommand) error { return errors.New("resolver unavailable") },
			want: "resolver unavailable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := gitFixture(t, map[string]string{
				"go.mod": "module example.com/demo\n\ngo 1.26\n\nrequire example.com/dependency v1.0.0\n",
				"go.sum": "before\n",
			})
			report := fixtureReport(t, root, "go.mod", ManagerGoMod, "example.com/dependency", "v1.0.0", "v1.1.0")
			_, err := applyWithLockfiles(context.Background(), root, report, true, time.Second, &fakeLockRunner{run: test.run})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
			assertFile(t, root, "go.mod", "v1.0.0")
			assertFile(t, root, "go.sum", "before\n")
		})
	}
}

func TestApplyWithLockfilesProtectsTargetStateButAllowsUnrelatedChanges(t *testing.T) {
	root := gitFixture(t, map[string]string{
		"go.mod":    "module example.com/demo\n\ngo 1.26\n\nrequire example.com/dependency v1.0.0\n",
		"go.sum":    "before\n",
		"README.md": "tracked\n",
	})
	report := fixtureReport(t, root, "go.mod", ManagerGoMod, "example.com/dependency", "v1.0.0", "v1.1.0")
	runner := &fakeLockRunner{run: func(_ int, command lockCommand) error {
		return os.WriteFile(filepath.Join(command.dir, "go.sum"), []byte("stable\n"), 0o644)
	}}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("unrelated local work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := applyWithLockfiles(context.Background(), root, report, false, time.Second, runner); err != nil {
		t.Fatalf("unrelated dirty file rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.sum"), []byte("dirty target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := applyWithLockfiles(context.Background(), root, report, false, time.Second, runner); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("dirty target error=%v", err)
	}
}

func TestApplyWithLockfilesNeverOverwritesTargetChangedDuringRegeneration(t *testing.T) {
	root := gitFixture(t, map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.26\n\nrequire example.com/dependency v1.0.0\n",
		"go.sum": "before\n",
	})
	report := fixtureReport(t, root, "go.mod", ManagerGoMod, "example.com/dependency", "v1.0.0", "v1.1.0")
	runner := &fakeLockRunner{run: func(call int, command lockCommand) error {
		if call == 1 {
			if err := os.WriteFile(filepath.Join(root, "go.sum"), []byte("user change\n"), 0o644); err != nil {
				return err
			}
		}
		return os.WriteFile(filepath.Join(command.dir, "go.sum"), []byte("generated\n"), 0o644)
	}}
	_, err := applyWithLockfiles(context.Background(), root, report, true, time.Second, runner)
	if err == nil || !strings.Contains(err.Error(), "changed after planning") {
		t.Fatalf("source race accepted: %v", err)
	}
	assertFile(t, root, "go.mod", "v1.0.0")
	assertFile(t, root, "go.sum", "user change\n")
}

func TestApplyWithLockfilesRequiresPlanDigest(t *testing.T) {
	root := gitFixture(t, map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.26\n\nrequire example.com/dependency v1.0.0\n",
		"go.sum": "before\n",
	})
	report := fixtureReport(t, root, "go.mod", ManagerGoMod, "example.com/dependency", "v1.0.0", "v1.1.0")
	report.PlanDigest = ""
	if _, err := applyWithLockfiles(context.Background(), root, report, false, time.Second, &fakeLockRunner{}); err == nil || !strings.Contains(err.Error(), "plan digest") {
		t.Fatalf("unbound report accepted: %v", err)
	}
}

func TestApplyWithLockfilesCreatesNewLockfile(t *testing.T) {
	root := gitFixture(t, map[string]string{
		"package.json": "{\"packageManager\":\"npm@12.0.2\",\"dependencies\":{\"demo\":\"1.0.0\"}}\n",
	})
	report := fixtureReport(t, root, "package.json", ManagerNPM, "demo", "1.0.0", "1.1.0")
	runner := &fakeLockRunner{run: func(_ int, command lockCommand) error {
		return os.WriteFile(filepath.Join(command.dir, "package-lock.json"), []byte("{\"lockfileVersion\":3}\n"), 0o644)
	}}
	files, err := applyWithLockfiles(context.Background(), root, report, true, time.Second, runner)
	if err != nil {
		t.Fatal(err)
	}
	assertAppliedPaths(t, files, "package-lock.json", "package.json")
	if !files[0].Created || files[0].Kind != "lockfile" {
		t.Fatalf("new lockfile metadata=%+v", files[0])
	}
	assertFile(t, root, "package-lock.json", "lockfileVersion")
}

func TestApplyWithLockfilesRejectsGitContentFilters(t *testing.T) {
	root := gitFixture(t, map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.26\n\nrequire example.com/dependency v1.0.0\n",
		"go.sum": "before\n",
	})
	gitRun(t, root, "config", "filter.untrusted.clean", "sh exploit.sh")
	report := fixtureReport(t, root, "go.mod", ManagerGoMod, "example.com/dependency", "v1.0.0", "v1.1.0")
	_, err := applyWithLockfiles(context.Background(), root, report, false, time.Second, &fakeLockRunner{})
	if err == nil || !strings.Contains(err.Error(), "content filters are not allowed") {
		t.Fatalf("error=%v", err)
	}
}

func TestApplyWithLockfilesRejectsRepositoryCargoConfiguration(t *testing.T) {
	root := gitFixture(t, map[string]string{
		"Cargo.toml":         "[package]\nname = \"demo\"\nversion = \"0.1.0\"\n\n[dependencies]\nitoa = \"1.0.14\"\n",
		"Cargo.lock":         "before\n",
		".cargo/config.toml": "[registry]\nglobal-credential-providers = [\"cargo:token\"]\n",
	})
	report := fixtureReport(t, root, "Cargo.toml", ManagerCargo, "itoa", "1.0.14", "1.0.15")
	_, err := applyWithLockfiles(context.Background(), root, report, false, time.Second, &fakeLockRunner{})
	if err == nil || !strings.Contains(err.Error(), "Cargo configuration is not allowed") {
		t.Fatalf("error=%v", err)
	}
}

func TestNuGetRegenerationUsesSyntheticProject(t *testing.T) {
	root := gitFixture(t, map[string]string{
		"demo.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net10.0</TargetFramework></PropertyGroup>
  <ItemGroup><PackageReference Include="Example.Package" Version="1.0.0" /></ItemGroup>
  <Target Name="Untrusted" BeforeTargets="Restore"><Exec Command="touch should-not-run" /></Target>
</Project>
`,
	})
	report := fixtureReport(t, root, "demo.csproj", ManagerNuGet, "Example.Package", "1.0.0", "1.1.0")
	runner := &fakeLockRunner{run: func(_ int, command lockCommand) error {
		if command.name != "dotnet" || len(command.args) < 2 || command.args[0] != "restore" {
			return fmt.Errorf("unexpected command: %#v", command)
		}
		synthetic, err := os.ReadFile(command.args[1])
		if err != nil {
			return err
		}
		if bytes.Contains(synthetic, []byte("Exec")) || bytes.Contains(synthetic, []byte("should-not-run")) {
			return errors.New("repository target copied into synthetic project")
		}
		if !bytes.Contains(synthetic, []byte(`PackageReference Include="Example.Package" Version="1.1.0"`)) {
			return fmt.Errorf("updated package missing from synthetic project: %s", synthetic)
		}
		return os.WriteFile(command.destinationPath, []byte("{\"version\":1}\n"), 0o644)
	}}
	files, err := applyWithLockfiles(context.Background(), root, report, true, time.Second, runner)
	if err != nil {
		t.Fatal(err)
	}
	assertAppliedPaths(t, files, "demo.csproj", "packages.lock.json")
	if _, err := os.Stat(filepath.Join(root, "should-not-run")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository command ran: %v", err)
	}
}

func TestNuGetSanitizerRejectsDynamicAndConditionalInputs(t *testing.T) {
	for _, project := range []string{
		`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>$(Framework)</TargetFramework></PropertyGroup><ItemGroup><PackageReference Include="Demo" Version="1.0.0" /></ItemGroup></Project>`,
		`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework></PropertyGroup><ItemGroup Condition="'$(X)' == 'y'"><PackageReference Include="Demo" Version="1.0.0" /></ItemGroup></Project>`,
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "demo.csproj"), []byte(project), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := sanitizedNuGetProject(root, t.TempDir(), "demo.csproj")
		if err == nil {
			t.Fatalf("unsafe project accepted: %s", project)
		}
	}
}

func fixtureReport(t *testing.T, root, file string, manager Manager, name, current, latest string) Report {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(data), current)
	if start < 0 {
		t.Fatalf("%s not found in %s", current, file)
	}
	entry := Update{Candidate: Candidate{
		Manager: manager, Datasource: string(manager), Name: name, CurrentVersion: current,
		CurrentValue: current, File: file, Line: 1 + strings.Count(string(data[:start]), "\n"), Start: start, End: start + len(current),
	}, LatestVersion: latest, UpdateType: "minor", Status: "outdated"}
	return Report{SchemaVersion: 2, Root: filepath.ToSlash(root), Updates: []Update{entry}, PlanDigest: planDigest([]Update{entry})}
}

func gitFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, "init", "-b", "main")
	gitRun(t, root, "config", "user.name", "HooNeedsUpdates Test")
	gitRun(t, root, "config", "user.email", "test@example.invalid")
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-m", "fixture")
	return root
}

func gitRun(t *testing.T, root string, arguments ...string) {
	t.Helper()
	if output, err := runGit(context.Background(), root, "", arguments...); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func assertAppliedPaths(t *testing.T, files []AppliedFile, expected ...string) {
	t.Helper()
	if len(files) != len(expected) {
		t.Fatalf("files=%v, want %v", files, expected)
	}
	for index := range expected {
		if files[index].Path != expected[index] {
			t.Fatalf("files[%d].Path=%q, want %q", index, files[index].Path, expected[index])
		}
	}
}

func assertFile(t *testing.T, root, name, contains string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), contains) {
		t.Fatalf("%s=%q, want substring %q", name, data, contains)
	}
}
