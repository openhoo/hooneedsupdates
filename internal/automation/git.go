package automation

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openhoo/hooneedsupdates/internal/update"
)

const maxGitOutput = 1 << 20

type gitVCS struct {
	token string
}

func (g gitVCS) Clone(ctx context.Context, repository repository, destination string) error {
	remote, err := url.Parse(repository.CloneURL)
	if err != nil || remote.Scheme != "https" || remote.Host == "" || remote.User != nil {
		return fmt.Errorf("repository %s returned an unsafe HTTPS clone URL", repository.FullName)
	}
	if repository.DefaultBranch == "" {
		return fmt.Errorf("repository %s has no default branch", repository.FullName)
	}
	hooks := filepath.Join(filepath.Dir(destination), "hooks")
	if err := os.Mkdir(hooks, 0o700); err != nil {
		return err
	}
	_, err = g.run(ctx, "", remote, nil,
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath="+hooks,
		"clone", "--no-tags", "--single-branch", "--branch", repository.DefaultBranch,
		"--", repository.CloneURL, destination,
	)
	if err != nil {
		return err
	}
	_, err = g.run(ctx, destination, remote, nil, "config", "--local", "core.hooksPath", hooks)
	return err
}

func (g gitVCS) Head(ctx context.Context, root string) (string, error) {
	output, err := g.runRepository(ctx, root, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(output))
	if len(sha) != 40 {
		return "", fmt.Errorf("git returned invalid HEAD SHA %q", sha)
	}
	return sha, nil
}

func (g gitVCS) Commit(
	ctx context.Context,
	root, message, author, email string,
	files []update.AppliedFile,
) (string, []string, error) {
	expected, paths, err := expectedPaths(files)
	if err != nil {
		return "", nil, err
	}
	status, err := g.runRepository(ctx, root, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "", nil, err
	}
	changed, err := parseStatus(status)
	if err != nil {
		return "", nil, err
	}
	if err := exactPathSet(changed, expected); err != nil {
		return "", nil, fmt.Errorf("refusing unexpected repository changes: %w", err)
	}
	arguments := append([]string{"add", "--"}, paths...)
	if _, err := g.runRepository(ctx, root, nil, arguments...); err != nil {
		return "", nil, err
	}
	if _, err := g.runRepository(ctx, root, nil, "diff", "--cached", "--check"); err != nil {
		return "", nil, err
	}
	baseDate, err := g.runRepository(ctx, root, nil, "show", "-s", "--format=%cI", "HEAD")
	if err != nil {
		return "", nil, err
	}
	date := strings.TrimSpace(string(baseDate))
	environment := map[string]string{
		"GIT_AUTHOR_NAME":     author,
		"GIT_AUTHOR_EMAIL":    email,
		"GIT_AUTHOR_DATE":     date,
		"GIT_COMMITTER_NAME":  author,
		"GIT_COMMITTER_EMAIL": email,
		"GIT_COMMITTER_DATE":  date,
	}
	if _, err := g.runRepository(ctx, root, environment,
		"-c", "commit.gpgSign=false", "commit", "--no-verify", "-m", message,
	); err != nil {
		return "", nil, err
	}
	sha, err := g.Head(ctx, root)
	return sha, paths, err
}

func (g gitVCS) Push(ctx context.Context, root, branch, expectedRemoteSHA string) error {
	arguments := []string{"push"}
	if expectedRemoteSHA != "" {
		arguments = append(arguments, "--force-with-lease=refs/heads/"+branch+":"+expectedRemoteSHA)
	}
	arguments = append(arguments, "origin", "HEAD:refs/heads/"+branch)
	_, err := g.runRepository(ctx, root, nil, arguments...)
	return err
}

func (g gitVCS) runRepository(ctx context.Context, root string, extra map[string]string, arguments ...string) ([]byte, error) {
	remoteOutput, err := g.run(ctx, root, nil, nil, "remote", "get-url", "origin")
	if err != nil && !(len(arguments) >= 2 && arguments[0] == "remote" && arguments[1] == "get-url") {
		return nil, err
	}
	var remote *url.URL
	if err == nil {
		remote, err = url.Parse(strings.TrimSpace(string(remoteOutput)))
		if err != nil {
			return nil, err
		}
	}
	base := []string{"-c", "core.fsmonitor=false", "-C", root}
	base = append(base, arguments...)
	return g.run(ctx, "", remote, extra, base...)
}

func (g gitVCS) run(
	ctx context.Context,
	directory string,
	remote *url.URL,
	extra map[string]string,
	arguments ...string,
) ([]byte, error) {
	executable, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, executable, arguments...)
	if directory != "" {
		command.Dir = directory
	}
	environment := map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_LFS_SKIP_SMUDGE": "1",
		"GIT_TERMINAL_PROMPT": "0",
	}
	for key, value := range extra {
		environment[key] = value
	}
	if g.token != "" && remote != nil {
		if remote.Scheme != "https" || remote.Host == "" || remote.User != nil {
			return nil, errors.New("refusing credentials for a non-HTTPS Git remote")
		}
		credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + g.token))
		environment["GIT_CONFIG_COUNT"] = "1"
		environment["GIT_CONFIG_KEY_0"] = "http." + remote.Scheme + "://" + remote.Host + "/.extraheader"
		environment["GIT_CONFIG_VALUE_0"] = "Authorization: Basic " + credential
	}
	command.Env = cleanEnvironment(environment)
	output := &boundedBuffer{remaining: maxGitOutput}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		if message != "" {
			return output.Bytes(), fmt.Errorf("git %s: %w: %s", arguments[0], err, message)
		}
		return output.Bytes(), fmt.Errorf("git %s: %w", arguments[0], err)
	}
	return output.Bytes(), nil
}

func expectedPaths(files []update.AppliedFile) (map[string]bool, []string, error) {
	expected := make(map[string]bool, len(files))
	for _, file := range files {
		raw := filepath.ToSlash(file.Path)
		path := pathpkg.Clean(raw)
		if unsafeAppliedPath(raw, path) || expected[path] {
			return nil, nil, fmt.Errorf("invalid or duplicate applied path %q", file.Path)
		}
		expected[path] = true
	}
	paths := make([]string, 0, len(expected))
	for path := range expected {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return expected, paths, nil
}

func unsafeAppliedPath(raw, cleaned string) bool {
	if raw == "" || strings.Contains(raw, `\`) || pathpkg.IsAbs(cleaned) {
		return true
	}
	for _, character := range raw {
		if character == 0 || character == '\n' || character == '\r' {
			return true
		}
	}
	return cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../")
}

func parseStatus(data []byte) (map[string]bool, error) {
	changed := map[string]bool{}
	for len(data) > 0 {
		end := bytes.IndexByte(data, 0)
		if end < 0 {
			return nil, errors.New("unterminated git status record")
		}
		record := string(data[:end])
		data = data[end+1:]
		if len(record) < 4 || record[2] != ' ' {
			return nil, fmt.Errorf("invalid git status record %q", record)
		}
		status, path := record[:2], filepath.ToSlash(record[3:])
		if status != " M" && status != "??" {
			return nil, fmt.Errorf("unsupported git status %q for %s", status, path)
		}
		if path == "" || changed[path] {
			return nil, fmt.Errorf("invalid or duplicate changed path %q", path)
		}
		changed[path] = true
	}
	return changed, nil
}

func exactPathSet(actual, expected map[string]bool) error {
	var unexpected, missing []string
	for path := range actual {
		if !expected[path] {
			unexpected = append(unexpected, path)
		}
	}
	for path := range expected {
		if !actual[path] {
			missing = append(missing, path)
		}
	}
	sort.Strings(unexpected)
	sort.Strings(missing)
	if len(unexpected) > 0 || len(missing) > 0 {
		return fmt.Errorf("unexpected=%v missing=%v", unexpected, missing)
	}
	return nil
}

func cleanEnvironment(overrides map[string]string) []string {
	blocked := map[string]bool{
		"GIT_CONFIG_COUNT": true, "GIT_CONFIG_GLOBAL": true,
		"GIT_LFS_SKIP_SMUDGE": true, "GIT_TERMINAL_PROMPT": true,
	}
	for key := range overrides {
		blocked[key] = true
	}
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if blocked[key] || strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			continue
		}
		result = append(result, entry)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, key+"="+overrides[key])
	}
	return result
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.remaining {
		written := b.remaining
		if written > 0 {
			_, _ = b.buffer.Write(data[:written])
			b.remaining = 0
		}
		return written, errors.New("command output exceeded 1 MiB")
	}
	b.remaining -= len(data)
	return b.buffer.Write(data)
}

func (b *boundedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *boundedBuffer) String() string { return b.buffer.String() }

var _ io.Writer = (*boundedBuffer)(nil)
