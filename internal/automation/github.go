package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxGitHubResponse = 8 << 20

var errNotFound = errors.New("GitHub resource not found")

type githubHost struct {
	client     *http.Client
	apiURL     string
	graphqlURL string
	token      string
}

func newGitHubHost(client *http.Client, apiURL, graphqlURL, token string) (*githubHost, error) {
	if client == nil {
		client = http.DefaultClient
	}
	apiURL = strings.TrimRight(apiURL, "/")
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	parsed, err := url.Parse(apiURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("GitHub API URL must be an HTTPS URL without credentials")
	}
	if graphqlURL == "" {
		graphqlURL = inferredGraphQLURL(parsed)
	}
	graphql, err := url.Parse(graphqlURL)
	if err != nil || graphql.Scheme != "https" || graphql.Host == "" || graphql.User != nil {
		return nil, fmt.Errorf("GitHub GraphQL URL must be an HTTPS URL without credentials")
	}
	if !strings.EqualFold(graphql.Host, parsed.Host) {
		return nil, errors.New("GitHub REST and GraphQL URLs must use the same host")
	}
	return &githubHost{client: client, apiURL: apiURL, graphqlURL: strings.TrimRight(graphqlURL, "/"), token: token}, nil
}

func inferredGraphQLURL(api *url.URL) string {
	if api.Host == "api.github.com" {
		return "https://api.github.com/graphql"
	}
	path := strings.TrimSuffix(api.Path, "/api/v3")
	return (&url.URL{Scheme: api.Scheme, Host: api.Host, Path: path + "/api/graphql"}).String()
}

func (g *githubHost) Repository(ctx context.Context, name string) (repository, error) {
	var result repository
	err := g.rest(ctx, http.MethodGet, g.repositoryPath(name), nil, &result)
	return result, err
}

func (g *githubHost) Ref(ctx context.Context, name, branch string) (string, bool, error) {
	var result struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	path := g.repositoryPath(name) + "/git/ref/heads/" + url.PathEscape(branch)
	if err := g.rest(ctx, http.MethodGet, path, nil, &result); err != nil {
		if errors.Is(err, errNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	if len(result.Object.SHA) != 40 {
		return "", false, fmt.Errorf("GitHub returned an invalid branch SHA for %s:%s", name, branch)
	}
	return result.Object.SHA, true, nil
}

func (g *githubHost) Pulls(ctx context.Context, name, owner, branch, base string) ([]pullRequest, error) {
	query := url.Values{}
	query.Set("state", "all")
	query.Set("head", owner+":"+branch)
	query.Set("base", base)
	query.Set("sort", "created")
	query.Set("direction", "desc")
	query.Set("per_page", "100")
	var result []pullRequest
	err := g.rest(ctx, http.MethodGet, g.repositoryPath(name)+"/pulls?"+query.Encode(), nil, &result)
	return result, err
}

func (g *githubHost) CreatePull(ctx context.Context, name, title, body, head, base string, draft bool) (pullRequest, error) {
	payload := map[string]any{"title": title, "body": body, "head": head, "base": base, "draft": draft}
	var result pullRequest
	err := g.rest(ctx, http.MethodPost, g.repositoryPath(name)+"/pulls", payload, &result)
	return result, err
}

func (g *githubHost) UpdatePull(ctx context.Context, name string, number int, title, body, base string) (pullRequest, error) {
	payload := map[string]any{"title": title, "body": body, "base": base}
	var result pullRequest
	err := g.rest(ctx, http.MethodPatch, fmt.Sprintf("%s/pulls/%d", g.repositoryPath(name), number), payload, &result)
	return result, err
}

func (g *githubHost) ClosePull(ctx context.Context, name string, number int) error {
	return g.rest(ctx, http.MethodPatch, fmt.Sprintf("%s/pulls/%d", g.repositoryPath(name), number), map[string]string{"state": "closed"}, nil)
}

func (g *githubHost) DeleteRef(ctx context.Context, name, branch string) error {
	path := g.repositoryPath(name) + "/git/refs/heads/" + url.PathEscape(branch)
	err := g.rest(ctx, http.MethodDelete, path, nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func (g *githubHost) AddLabels(ctx context.Context, name string, number int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	path := fmt.Sprintf("%s/issues/%d/labels", g.repositoryPath(name), number)
	return g.rest(ctx, http.MethodPost, path, map[string]any{"labels": labels}, nil)
}

func (g *githubHost) EnableAutoMerge(ctx context.Context, pullRequestID, method string) error {
	query := `mutation($input: EnablePullRequestAutoMergeInput!) {
  enablePullRequestAutoMerge(input: $input) { pullRequest { id } }
}`
	return g.graphql(ctx, query, map[string]any{"input": map[string]any{
		"pullRequestId": pullRequestID,
		"mergeMethod":   strings.ToUpper(method),
	}})
}

func (g *githubHost) DisableAutoMerge(ctx context.Context, pullRequestID string) error {
	query := `mutation($input: DisablePullRequestAutoMergeInput!) {
  disablePullRequestAutoMerge(input: $input) { pullRequest { id } }
}`
	return g.graphql(ctx, query, map[string]any{"input": map[string]any{"pullRequestId": pullRequestID}})
}

func (g *githubHost) graphql(ctx context.Context, query string, variables map[string]any) error {
	payload := map[string]any{"query": query, "variables": variables}
	var result struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := g.request(ctx, http.MethodPost, g.graphqlURL, payload, &result); err != nil {
		return err
	}
	if len(result.Errors) > 0 {
		messages := make([]string, 0, len(result.Errors))
		for _, entry := range result.Errors {
			messages = append(messages, entry.Message)
		}
		return fmt.Errorf("GitHub GraphQL: %s", strings.Join(messages, "; "))
	}
	return nil
}

func (g *githubHost) rest(ctx context.Context, method, path string, input, output any) error {
	return g.request(ctx, method, g.apiURL+path, input, output)
}

func (g *githubHost) request(ctx context.Context, method, endpoint string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "hooneedsupdates")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if g.token != "" {
		request.Header.Set("Authorization", "Bearer "+g.token)
	}
	response, err := g.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxGitHubResponse+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(data) > maxGitHubResponse {
		return errors.New("GitHub response exceeded 8 MiB")
	}
	if response.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return githubStatusError(method, request.URL.Path, response.StatusCode, data)
	}
	if output == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func githubStatusError(method, path string, status int, data []byte) error {
	var message struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &message)
	if message.Message == "" {
		message.Message = http.StatusText(status)
	}
	return fmt.Errorf("GitHub %s %s: HTTP %d: %s", method, path, status, message.Message)
}

func (g *githubHost) repositoryPath(name string) string {
	parts := strings.SplitN(name, "/", 2)
	return "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
}
