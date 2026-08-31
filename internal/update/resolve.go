package update

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

type Resolver interface {
	Resolve(context.Context, Candidate, bool) (Resolution, error)
}

type HTTPResolver struct {
	Client      *http.Client
	Token       string
	GitHubAPI   string
	GoProxy     string
	CratesAPI   string
	NPMRegistry string
	NuGetAPI    string
	DockerHub   string
}

func NewHTTPResolver(client *http.Client) *HTTPResolver {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	return &HTTPResolver{
		Client: client, Token: token,
		GitHubAPI: "https://api.github.com", GoProxy: "https://proxy.golang.org",
		CratesAPI: "https://crates.io/api/v1", NPMRegistry: "https://registry.npmjs.org",
		NuGetAPI: "https://api.nuget.org/v3-flatcontainer", DockerHub: "https://hub.docker.com/v2",
	}
}

func (r *HTTPResolver) Resolve(ctx context.Context, entry Candidate, includePrereleases bool) (Resolution, error) {
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
		return r.resolveDocker(ctx, entry.Name, entry.CurrentVersion, includePrereleases)
	default:
		return Resolution{}, fmt.Errorf("unsupported datasource %q", entry.Datasource)
	}
}

func (r *HTTPResolver) resolveGo(ctx context.Context, name string, includePrereleases bool) (Resolution, error) {
	escaped, err := module.EscapePath(name)
	if err != nil {
		return Resolution{}, err
	}
	body, listErr := r.get(ctx, r.endpoint(r.GoProxy, escaped+"/@v/list"), false)
	versions := strings.Fields(string(body))
	latestBody, latestErr := r.get(ctx, r.endpoint(r.GoProxy, escaped+"/@latest"), false)
	if latestErr == nil {
		var latest struct {
			Version string `json:"Version"`
		}
		if err := json.Unmarshal(latestBody, &latest); err != nil {
			return Resolution{}, err
		}
		if latest.Version != "" {
			versions = append(versions, latest.Version)
		}
	}
	if len(versions) == 0 {
		if listErr != nil {
			return Resolution{}, listErr
		}
		return Resolution{}, latestErr
	}
	return chooseGoVersion(versions, includePrereleases)
}

func (r *HTTPResolver) resolveCrate(ctx context.Context, name string, includePrereleases bool) (Resolution, error) {
	body, err := r.get(ctx, r.endpoint(r.CratesAPI, "crates/"+url.PathEscape(name)), false)
	if err != nil {
		return Resolution{}, err
	}
	var response struct {
		Versions []struct {
			Number string `json:"num"`
			Yanked bool   `json:"yanked"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return Resolution{}, err
	}
	versions := make([]string, 0, len(response.Versions))
	for _, version := range response.Versions {
		if !version.Yanked {
			versions = append(versions, version.Number)
		}
	}
	return chooseVersion(versions, includePrereleases)
}

func (r *HTTPResolver) resolveNPM(ctx context.Context, name string, includePrereleases bool) (Resolution, error) {
	if !includePrereleases {
		body, err := r.get(ctx, r.endpoint(r.NPMRegistry, url.PathEscape(name)+"/latest"), false)
		if err != nil {
			return Resolution{}, err
		}
		var latest struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(body, &latest); err != nil {
			return Resolution{}, err
		}
		if latest.Version == "" {
			return Resolution{}, errors.New("npm latest response has no version")
		}
		return Resolution{Version: latest.Version}, nil
	}
	body, err := r.get(ctx, r.endpoint(r.NPMRegistry, url.PathEscape(name)), false)
	if err != nil {
		return Resolution{}, err
	}
	var response struct {
		DistTags map[string]string          `json:"dist-tags"`
		Versions map[string]json.RawMessage `json:"versions"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return Resolution{}, err
	}
	if latest := response.DistTags["latest"]; latest != "" && (!strings.Contains(latest, "-") || includePrereleases) {
		return Resolution{Version: latest}, nil
	}
	versions := make([]string, 0, len(response.Versions))
	for version := range response.Versions {
		versions = append(versions, version)
	}
	return chooseVersion(versions, includePrereleases)
}

func (r *HTTPResolver) resolveNuGet(ctx context.Context, name string, includePrereleases bool) (Resolution, error) {
	endpoint := r.endpoint(r.NuGetAPI, strings.ToLower(url.PathEscape(name))+"/index.json")
	body, err := r.get(ctx, endpoint, false)
	if err != nil {
		return Resolution{}, err
	}
	var response struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return Resolution{}, err
	}
	return chooseVersion(response.Versions, includePrereleases)
}

func (r *HTTPResolver) resolveGitHub(ctx context.Context, name string, includePrereleases bool) (Resolution, error) {
	if strings.Count(name, "/") != 1 {
		return Resolution{}, fmt.Errorf("invalid GitHub repository %q", name)
	}
	tag, err := r.latestGitHubTag(ctx, name, includePrereleases)
	if err != nil {
		return Resolution{}, err
	}
	digest, err := r.githubTagDigest(ctx, name, tag)
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{Version: tag, Digest: digest}, nil
}

func (r *HTTPResolver) latestGitHubTag(ctx context.Context, name string, includePrereleases bool) (string, error) {
	body, err := r.get(ctx, r.endpoint(r.GitHubAPI, "repos/"+name+"/releases/latest"), true)
	if err == nil {
		var release struct {
			Tag string `json:"tag_name"`
		}
		if decodeErr := json.Unmarshal(body, &release); decodeErr != nil {
			return "", decodeErr
		}
		if release.Tag != "" {
			return release.Tag, nil
		}
	}
	body, tagsErr := r.get(ctx, r.endpoint(r.GitHubAPI, "repos/"+name+"/tags?per_page=100"), true)
	if tagsErr != nil {
		if err != nil {
			return "", err
		}
		return "", tagsErr
	}
	var tags []struct {
		Name string `json:"name"`
	}
	if decodeErr := json.Unmarshal(body, &tags); decodeErr != nil {
		return "", decodeErr
	}
	versions := make([]string, 0, len(tags))
	for _, entry := range tags {
		versions = append(versions, entry.Name)
	}
	chosen, chooseErr := chooseVersion(versions, includePrereleases)
	return chosen.Version, chooseErr
}

func (r *HTTPResolver) githubTagDigest(ctx context.Context, name, tag string) (string, error) {
	endpoint := r.endpoint(r.GitHubAPI, "repos/"+name+"/git/ref/tags/"+url.PathEscape(tag))
	body, err := r.get(ctx, endpoint, true)
	if err != nil {
		return "", err
	}
	var ref struct {
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(body, &ref); err != nil {
		return "", err
	}
	for depth := 0; ref.Object.Type == "tag" && depth < 5; depth++ {
		body, err = r.get(ctx, r.endpoint(r.GitHubAPI, "repos/"+name+"/git/tags/"+ref.Object.SHA), true)
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal(body, &ref); err != nil {
			return "", err
		}
	}
	if ref.Object.Type != "commit" || len(ref.Object.SHA) != 40 {
		return "", fmt.Errorf("tag %s for %s did not resolve to a commit", tag, name)
	}
	return ref.Object.SHA, nil
}

func (r *HTTPResolver) resolveDocker(ctx context.Context, name, current string, includePrereleases bool) (Resolution, error) {
	if strings.Contains(strings.Split(name, "/")[0], ".") || strings.Contains(name, ":") {
		return Resolution{}, fmt.Errorf("registry for %s is not supported yet", name)
	}
	if !strings.Contains(name, "/") {
		name = "library/" + name
	}
	endpoint := r.endpoint(r.DockerHub, "repositories/"+name+"/tags?page_size=100")
	tags, err := r.dockerTags(ctx, endpoint)
	if err != nil {
		return Resolution{}, err
	}
	suffix := dockerSuffix(current)
	minimumDots := dockerVersionDots(current)
	filtered := make([]string, 0, len(tags))
	for _, tag := range tags {
		if dockerSuffix(tag) == suffix && dockerVersionDots(tag) >= minimumDots {
			filtered = append(filtered, tag)
		}
	}
	return chooseVersion(filtered, includePrereleases || suffix != "")
}

func (r *HTTPResolver) dockerTags(ctx context.Context, endpoint string) ([]string, error) {
	var tags []string
	for page := 0; endpoint != "" && page < 5; page++ {
		body, err := r.get(ctx, endpoint, false)
		if err != nil {
			return nil, err
		}
		var response struct {
			Next    string `json:"next"`
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, err
		}
		for _, result := range response.Results {
			tags = append(tags, result.Name)
		}
		endpoint = response.Next
	}
	return tags, nil
}

func dockerVersionDots(tag string) int {
	match := numericVersion.FindStringSubmatch(tag)
	if match == nil {
		return -1
	}
	return strings.Count(match[1], ".")
}

func dockerSuffix(tag string) string {
	match := numericVersion.FindStringSubmatch(tag)
	if match == nil {
		return ""
	}
	return match[2]
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

func chooseGoVersion(versions []string, includePrereleases bool) (Resolution, error) {
	var chosen string
	for _, version := range versions {
		if !semver.IsValid(version) {
			continue
		}
		if !includePrereleases && semver.Prerelease(version) != "" && !module.IsPseudoVersion(version) {
			continue
		}
		if chosen == "" || semver.Compare(version, chosen) > 0 {
			chosen = version
		}
	}
	if chosen == "" {
		return Resolution{}, errors.New("no compatible Go module version found")
	}
	return Resolution{Version: chosen}, nil
}

func (r *HTTPResolver) get(ctx context.Context, endpoint string, github bool) ([]byte, error) {
	if r.Client == nil {
		return nil, errors.New("HTTP client is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "hooneedsupdates/0.1")
	if github {
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if r.Token != "" {
			request.Header.Set("Authorization", "Bearer "+r.Token)
		}
	}
	response, err := r.Client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := response.Status
		limited := io.LimitReader(response.Body, 4096)
		if body, readErr := io.ReadAll(limited); readErr == nil && len(body) > 0 {
			message += ": " + strings.TrimSpace(string(body))
		}
		return nil, errors.New(message)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 32<<20 {
		return nil, errors.New("response exceeds 32 MiB limit")
	}
	return body, nil
}

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
