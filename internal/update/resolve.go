package update

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/openhoo/hooneedsupdates/internal/githubapi"
	"golang.org/x/mod/semver"
)

type Resolver interface {
	Resolve(context.Context, Candidate, bool) (Resolution, error)
}

type HTTPResolver struct {
	Client       *http.Client
	GitHubClient *githubapi.Client
	Token        string
	GitHubAPI    string
	GoProxy      string
	CratesAPI    string
	NPMRegistry  string
	NuGetAPI     string
	DockerHub    string
}

func NewHTTPResolver(client *http.Client, token string) *HTTPResolver {
	return &HTTPResolver{
		Client: client, Token: token,
		GitHubAPI: "https://api.github.com", GoProxy: "https://proxy.golang.org",
		CratesAPI: "https://crates.io/api/v1", NPMRegistry: "https://registry.npmjs.org",
		NuGetAPI: "https://api.nuget.org/v3-flatcontainer", DockerHub: "https://hub.docker.com/v2",
	}
}

func (r *HTTPResolver) resolveVersion(ctx context.Context, entry Candidate, includePrereleases bool) (Resolution, error) {
	switch entry.Datasource {
	case "go":
		return r.resolveGo(ctx, entry.Name, includePrereleases)
	case "crates.io":
		return r.resolveCrate(ctx, entry.Name, includePrereleases)
	case "npm":
		return r.resolveNPM(ctx, entry.Name, includePrereleases)
	case "nuget":
		return r.resolveNuGet(ctx, entry.Name, includePrereleases)
	case "github-releases":
		return r.resolveGitHub(ctx, entry.Name, includePrereleases)
	case "docker":
		return r.resolveDockerCandidate(ctx, entry, includePrereleases)
	default:
		return Resolution{}, fmt.Errorf("unsupported datasource %q", entry.Datasource)
	}
}

func chooseVersion(versions []string, includePrereleases bool) (Resolution, error) {
	var chosen, chosenNormalized string
	for _, version := range versions {
		normalized := normalizeVersion(version)
		if normalized == "" || (!includePrereleases && semver.Prerelease(normalized) != "") {
			continue
		}
		if chosen == "" || semver.Compare(normalized, chosenNormalized) > 0 {
			chosen, chosenNormalized = version, normalized
		}
	}
	if chosen == "" {
		return Resolution{}, errors.New("no compatible stable version found")
	}
	return Resolution{Version: chosen}, nil
}

func (r *HTTPResolver) get(ctx context.Context, endpoint string, github bool) ([]byte, error) {
	body, _, err := r.getWithHeaders(ctx, endpoint, github)
	return body, err
}

func (r *HTTPResolver) getWithHeaders(ctx context.Context, endpoint string, github bool) ([]byte, http.Header, error) {
	if r.Client == nil {
		return nil, nil, errors.New("HTTP client is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "hooneedsupdates/0.1")
	if github {
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if r.Token != "" {
			request.Header.Set("Authorization", "Bearer "+r.Token)
		}
	}
	var response *http.Response
	if github && r.GitHubClient != nil {
		response, err = r.GitHubClient.Do(ctx, request)
	} else {
		response, err = r.Client.Do(request)
	}
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := response.Status
		limited := io.LimitReader(response.Body, 4096)
		if body, readErr := io.ReadAll(limited); readErr == nil && len(body) > 0 {
			message += ": " + strings.TrimSpace(string(body))
		}
		return nil, nil, &datasourceHTTPError{status: response.StatusCode, message: message}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
	if err != nil {
		return nil, nil, err
	}
	if len(body) > 32<<20 {
		return nil, nil, errors.New("response exceeds 32 MiB limit")
	}
	return body, response.Header.Clone(), nil
}

type datasourceHTTPError struct {
	status  int
	message string
}

func (e *datasourceHTTPError) Error() string { return e.message }

func (r *HTTPResolver) endpoint(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func readVersionLines(body []byte) []string {
	var result []string
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		result = append(result, strings.TrimSpace(scanner.Text()))
	}
	sort.Strings(result)
	return result
}
