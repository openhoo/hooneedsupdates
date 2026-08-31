package automation

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGitHubHostUsesAuthenticatedVersionedRequests(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer secret-token" {
			t.Fatalf("authorization=%q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Fatalf("api version=%q", request.Header.Get("X-GitHub-Api-Version"))
		}
		if request.URL.Path != "/repos/openhoo/tool" {
			t.Fatalf("path=%q", request.URL.Path)
		}
		return jsonResponse(http.StatusOK, `{"full_name":"openhoo/tool","default_branch":"main","clone_url":"https://github.com/openhoo/tool.git"}`), nil
	})
	host, err := newGitHubHost(&http.Client{Transport: transport}, "https://api.github.test", "https://api.github.test/graphql", "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	repository, err := host.Repository(context.Background(), "openhoo/tool")
	if err != nil || repository.FullName != "openhoo/tool" {
		t.Fatalf("repository=%+v error=%v", repository, err)
	}
}

func TestGitHubHostAutoMergeMutations(t *testing.T) {
	var bodies []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(data))
		return jsonResponse(http.StatusOK, `{"data":{"pullRequest":{"id":"PR_1"}}}`), nil
	})
	host, err := newGitHubHost(&http.Client{Transport: transport}, "https://api.github.test", "https://api.github.test/graphql", "token")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.EnableAutoMerge(context.Background(), "PR_1", "squash"); err != nil {
		t.Fatal(err)
	}
	if err := host.DisableAutoMerge(context.Background(), "PR_1"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], "EnablePullRequestAutoMergeInput") ||
		!strings.Contains(bodies[0], `"mergeMethod":"SQUASH"`) ||
		!strings.Contains(bodies[1], "DisablePullRequestAutoMergeInput") {
		t.Fatalf("unexpected GraphQL bodies: %v", bodies)
	}
}

func TestGitHubHostTreatsMissingRefAsAbsentAndBoundsErrors(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Path, "/git/ref/") {
			return jsonResponse(http.StatusNotFound, `{"message":"Not Found"}`), nil
		}
		return jsonResponse(http.StatusForbidden, `{"message":"denied"}`), nil
	})
	host, err := newGitHubHost(&http.Client{Transport: transport}, "https://api.github.test", "https://api.github.test/graphql", "token")
	if err != nil {
		t.Fatal(err)
	}
	if sha, exists, err := host.Ref(context.Background(), "openhoo/tool", "bot/updates"); err != nil || exists || sha != "" {
		t.Fatalf("sha=%q exists=%v error=%v", sha, exists, err)
	}
	if _, err := host.Repository(context.Background(), "openhoo/tool"); err == nil || !strings.Contains(err.Error(), "HTTP 403: denied") || strings.Contains(err.Error(), "token") {
		t.Fatalf("unexpected sanitized error: %v", err)
	}
}

func TestGitHubHostRejectsUnsafeEndpoints(t *testing.T) {
	for _, endpoint := range []string{"http://api.github.test", "https://user:secret@api.github.test", "not a URL"} {
		if _, err := newGitHubHost(http.DefaultClient, endpoint, "", "token"); err == nil {
			t.Fatalf("unsafe endpoint accepted: %q", endpoint)
		}
	}
	if _, err := newGitHubHost(
		http.DefaultClient,
		"https://api.github.test",
		"https://attacker.example/graphql",
		"token",
	); err == nil {
		t.Fatal("cross-host GraphQL endpoint accepted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
