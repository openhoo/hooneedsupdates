package update

import (
	"context"
	"os"
	"os/exec"
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
		t.Skipf("%s is unavailable", name)
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
