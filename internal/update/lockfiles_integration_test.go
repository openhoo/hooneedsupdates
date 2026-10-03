package update

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExecutableLockfileRegeneration(t *testing.T) {
	if os.Getenv("HOONEEDSUPDATE_INTEGRATION") != "1" {
		t.Skip("set HOONEEDSUPDATE_INTEGRATION=1 to run package-manager integration tests")
	}
	t.Run("cargo", func(t *testing.T) {
		requireExecutable(t, "cargo")
		root := gitFixture(t, map[string]string{
			"Cargo.toml": "[package]\nname = \"lockfile-fixture\"\nversion = \"0.1.0\"\nedition = \"2024\"\n\n[dependencies]\nitoa = \"1.0.14\"\n",
			"src/lib.rs": "pub fn fixture(value: i32) -> String { itoa::Buffer::new().format(value).to_owned() }\n",
		})
		runExternal(t, root, "cargo", "generate-lockfile")
		runExternal(t, root, "cargo", "update", "--package", "itoa", "--precise", "1.0.14")
		gitRun(t, root, "add", "Cargo.lock")
		gitRun(t, root, "commit", "-m", "add cargo lock")
		report := fixtureReport(t, root, "Cargo.toml", ManagerCargo, "itoa", "1.0.14", "1.0.15")
		files, err := applyWithLockfiles(context.Background(), root, report, true, 2*time.Minute, integrationRunner{t: t})
		if err != nil {
			t.Fatal(err)
		}
		assertAppliedPaths(t, files, "Cargo.lock", "Cargo.toml")
		assertFile(t, root, "Cargo.lock", "1.0.15")
		runExternal(t, root, "cargo", "check", "--locked")
	})

	t.Run("npm", func(t *testing.T) {
		requireExecutable(t, "npm")
		root := gitFixture(t, map[string]string{
			"package.json": "{\"name\":\"lockfile-fixture\",\"private\":true,\"packageManager\":\"npm@12.0.2\",\"dependencies\":{\"lodash\":\"4.17.20\"}}\n",
		})
		runExternal(t, root, "npm", "install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund", "--allow-git=none")
		gitRun(t, root, "add", "package-lock.json")
		gitRun(t, root, "commit", "-m", "add npm lock")
		report := fixtureReport(t, root, "package.json", ManagerNPM, "lodash", "4.17.20", "4.17.21")
		files, err := applyWithLockfiles(context.Background(), root, report, true, 2*time.Minute, integrationRunner{t: t})
		if err != nil {
			t.Fatal(err)
		}
		assertAppliedPaths(t, files, "package-lock.json", "package.json")
		assertFile(t, root, "package-lock.json", "4.17.21")
		runExternal(t, root, "npm", "install", "--package-lock-only", "--ignore-scripts", "--offline", "--no-audit", "--no-fund", "--allow-git=none")
	})
}

type integrationRunner struct{ t *testing.T }

func (runner integrationRunner) Run(ctx context.Context, command lockCommand) ([]byte, error) {
	runner.t.Logf("run %s %s", command.name, strings.Join(command.args, " "))
	output, err := (executableRunner{}).Run(ctx, command)
	if len(output) > 0 {
		runner.t.Logf("output: %s", output)
	}
	return output, err
}

func requireExecutable(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("required integration executable %s is unavailable", name)
	}
}

func runExternal(t *testing.T, directory, name string, arguments ...string) {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(arguments, " "), err, output)
	}
}

func TestExecutableAdditionalManagers(t *testing.T) {
	if os.Getenv("HOONEEDSUPDATE_INTEGRATION") != "1" {
		t.Skip("set HOONEEDSUPDATE_INTEGRATION=1")
	}
	t.Run("go-workspace", func(t *testing.T) {
		requireExecutable(t, "go")
		root := gitFixture(t, map[string]string{
			"go.work":        "go 1.25.0\n\nuse ./module\n",
			"module/go.mod":  "module example.test/fixture\n\ngo 1.25.0\n\nrequire golang.org/x/text v0.27.0\n",
			"module/main.go": "package fixture\nimport \"golang.org/x/text/cases\"\nvar _ = cases.Fold\n",
		})
		runExternal(t, filepath.Join(root, "module"), "go", "mod", "tidy")
		gitRun(t, root, "add", "module/go.sum")
		gitRun(t, root, "commit", "-m", "add go sums")
		report := fixtureReport(t, root, "module/go.mod", ManagerGoMod, "golang.org/x/text", "v0.27.0", "v0.28.0")
		files, err := applyWithLockfiles(context.Background(), root, report, true, 2*time.Minute, integrationRunner{t: t})
		if err != nil {
			t.Fatal(err)
		}
		assertAppliedPaths(t, files, "module/go.mod", "module/go.sum")
		runExternal(t, filepath.Join(root, "module"), "go", "test", "-mod=readonly", "./...")
	})
	t.Run("bun-no-scripts", func(t *testing.T) {
		requireExecutable(t, "bun")
		marker := filepath.Join(t.TempDir(), "script-executed")
		script := "node -e \"require('fs').writeFileSync(" + strconv.Quote(marker) + ",'bad')\""
		// A trusted dependency must still never execute its lifecycle script.
		packageJSON := map[string]any{"name": "lockfile-fixture", "private": true, "packageManager": "bun@1.3.14", "dependencies": map[string]string{"lodash": "4.17.20"}, "scripts": map[string]string{"postinstall": script}, "trustedDependencies": []string{"lodash"}}
		encoded, _ := json.Marshal(packageJSON)
		root := gitFixture(t, map[string]string{"package.json": string(encoded) + "\n"})
		runExternal(t, root, "bun", "install", "--lockfile-only", "--ignore-scripts")
		gitRun(t, root, "add", "bun.lock")
		gitRun(t, root, "commit", "-m", "add bun lock")
		report := fixtureReport(t, root, "package.json", ManagerNPM, "lodash", "4.17.20", "4.17.21")
		files, err := applyWithLockfiles(context.Background(), root, report, true, 2*time.Minute, integrationRunner{t: t})
		if err != nil {
			t.Fatal(err)
		}
		assertAppliedPaths(t, files, "bun.lock", "package.json")
		assertFile(t, root, "bun.lock", "4.17.21")
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("Bun executed a repository lifecycle script")
		}
	})
	t.Run("nuget-static-project", func(t *testing.T) {
		requireExecutable(t, "dotnet")
		marker := filepath.Join(t.TempDir(), "target-executed")
		project := `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework></PropertyGroup><ItemGroup><PackageReference Include="Newtonsoft.Json" Version="13.0.1" /></ItemGroup><Target Name="Trap" BeforeTargets="Restore"><WriteLinesToFile File="` + marker + `" Lines="executed" /></Target></Project>`
		root := gitFixture(t, map[string]string{"app.csproj": project, "Directory.Build.targets": `<Project><Target Name="TrapImported" BeforeTargets="Restore"><WriteLinesToFile File="` + marker + `" Lines="executed" /></Target></Project>`})
		report := fixtureReport(t, root, "app.csproj", ManagerNuGet, "Newtonsoft.Json", "13.0.1", "13.0.3")
		files, err := applyWithLockfiles(context.Background(), root, report, true, 3*time.Minute, integrationRunner{t: t})
		if err != nil {
			t.Fatal(err)
		}
		assertAppliedPaths(t, files, "app.csproj", "packages.lock.json")
		assertFile(t, root, "packages.lock.json", "13.0.3")
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("NuGet executed repository MSBuild targets")
		}
	})
}
