package setup

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallRejectsChecksumMismatch(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	script := filepath.Join(filepath.Dir(source), "install.sh")
	root := t.TempDir()
	mockBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(mockBin, 0o755); err != nil {
		t.Fatal(err)
	}

	cosignLog := filepath.Join(root, "cosign.log")
	tarMarker := filepath.Join(root, "tar.invoked")
	executableMarker := filepath.Join(root, "executable.invoked")
	writeExecutable(t, filepath.Join(mockBin, "curl"), `#!/usr/bin/env bash
set -euo pipefail
output=""
url=""
while (($#)); do
  if [[ "$1" == "--output" ]]; then
    output="$2"
    shift 2
    continue
  fi
  if [[ "$1" == https://* ]]; then
    url="$1"
  fi
  shift
done
case "$url" in
  */SHA256SUMS.sigstore.json) printf 'valid bundle\n' > "$output" ;;
  */SHA256SUMS) printf '%064d  hooneedsupdates_1.2.3_linux_amd64.tar.gz\n' 0 > "$output" ;;
  *.tar.gz.sigstore.json) printf 'valid bundle\n' > "$output" ;;
  *.tar.gz) printf 'archive contents\n' > "$output" ;;
  *) echo "unexpected URL: $url" >&2; exit 1 ;;
esac
`)
	writeExecutable(t, filepath.Join(mockBin, "cosign"), `#!/usr/bin/env bash
set -euo pipefail
printf 'verify-blob\n' >> "$COSIGN_LOG"
`)
	writeExecutable(t, filepath.Join(mockBin, "tar"), `#!/usr/bin/env bash
set -euo pipefail
destination=""
while (($#)); do
  if [[ "$1" == "-C" ]]; then
    destination="$2"
    shift 2
    continue
  fi
  shift
done
printf 'tar invoked\n' > "$TAR_MARKER"
mkdir -p "$destination"
cat > "$destination/hooneedsupdates" <<'EOF'
#!/usr/bin/env bash
printf 'executable invoked\n' >> "$EXECUTABLE_MARKER"
EOF
chmod +x "$destination/hooneedsupdates"
`)

	runnerTemp := filepath.Join(root, "runner-temp")
	if err := os.MkdirAll(runnerTemp, 0o755); err != nil {
		t.Fatal(err)
	}
	githubOutput := filepath.Join(root, "github-output")
	githubPath := filepath.Join(root, "github-path")
	command := exec.Command("bash", script)
	command.Env = installerTestEnv(map[string]string{
		"INPUT_VERSION":     "1.2.3",
		"RUNNER_OS_VALUE":   "Linux",
		"RUNNER_ARCH_VALUE": "X64",
		"RUNNER_TEMP":       runnerTemp,
		"GITHUB_OUTPUT":     githubOutput,
		"GITHUB_PATH":       githubPath,
		"PATH":              mockBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"COSIGN_LOG":        cosignLog,
		"TAR_MARKER":        tarMarker,
		"EXECUTABLE_MARKER": executableMarker,
	})
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("install succeeded for mismatched archive: %s", output)
	}
	if !strings.Contains(string(output), "::error::release checksum mismatch") {
		t.Fatalf("output=%s, want checksum mismatch error", output)
	}
	if !fileAbsent(t, tarMarker) {
		t.Fatal("tar was invoked after checksum mismatch")
	}
	if !fileAbsent(t, executableMarker) {
		t.Fatal("downloaded executable was invoked after checksum mismatch")
	}
	cosignOutput, readErr := os.ReadFile(cosignLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := strings.Count(string(cosignOutput), "verify-blob"); got != 2 {
		t.Fatalf("cosign verification count=%d, want 2", got)
	}
}

func installerTestEnv(overrides map[string]string) []string {
	env := os.Environ()
	for key := range overrides {
		prefix := key + "="
		filtered := env[:0]
		for _, entry := range env {
			if !strings.HasPrefix(entry, prefix) {
				filtered = append(filtered, entry)
			}
		}
		env = filtered
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func fileAbsent(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return false
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s: %v", path, err)
	}
	return true
}
