package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPResolverDatasources(t *testing.T) {
	commit := strings.Repeat("c", 40)
	tagObject := strings.Repeat("d", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/go/example.test/mod/@v/list":
			fmt.Fprint(writer, "v1.0.0\nv1.2.0\n")
		case "/go/example.test/mod/@latest":
			fmt.Fprint(writer, `{"Version":"v1.3.0"}`)
		case "/crates/crates/demo":
			fmt.Fprint(writer, `{"versions":[{"num":"2.0.0-rc.1","yanked":false},{"num":"1.4.0","yanked":false},{"num":"9.0.0","yanked":true}]}`)
		case "/npm/demo/latest":
			fmt.Fprint(writer, `{"version":"3.2.1"}`)
		case "/nuget/demo/index.json":
			fmt.Fprint(writer, `{"versions":["1.0.0","2.0.0-beta.1","1.8.0"]}`)
		case "/github/repos/owner/action/releases/latest":
			fmt.Fprint(writer, `{"tag_name":"v4.2.0"}`)
		case "/github/repos/owner/action/git/ref/tags/v4.2.0":
			fmt.Fprintf(writer, `{"object":{"type":"tag","sha":"%s"}}`, tagObject)
		case "/github/repos/owner/action/git/tags/" + tagObject:
			fmt.Fprintf(writer, `{"object":{"type":"commit","sha":"%s"}}`, commit)
		case "/docker/repositories/library/alpine/tags":
			fmt.Fprint(writer, `{"next":"","results":[{"name":"20260805"},{"name":"3.23.0"},{"name":"3.24.1"}]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	resolver := &HTTPResolver{
		Client: server.Client(), GitHubAPI: server.URL + "/github", GoProxy: server.URL + "/go",
		CratesAPI: server.URL + "/crates", NPMRegistry: server.URL + "/npm",
		NuGetAPI: server.URL + "/nuget", DockerHub: server.URL + "/docker",
	}
	tests := []struct {
		candidate Candidate
		version   string
		digest    string
	}{
		{Candidate{Datasource: "go", Name: "example.test/mod"}, "v1.3.0", ""},
		{Candidate{Datasource: "crates.io", Name: "demo"}, "1.4.0", ""},
		{Candidate{Datasource: "npm", Name: "demo"}, "3.2.1", ""},
		{Candidate{Datasource: "nuget", Name: "Demo"}, "1.8.0", ""},
		{Candidate{Datasource: "github-releases", Name: "owner/action"}, "v4.2.0", commit},
		{Candidate{Datasource: "docker", Name: "alpine", CurrentVersion: "3.22"}, "3.24.1", ""},
	}
	for _, test := range tests {
		resolution, err := resolver.Resolve(context.Background(), test.candidate, false)
		if err != nil {
			t.Fatalf("resolve %s: %v", test.candidate.Datasource, err)
		}
		if resolution.Version != test.version || resolution.Digest != test.digest {
			t.Fatalf("resolve %s = %#v, want version %s digest %s", test.candidate.Datasource, resolution, test.version, test.digest)
		}
	}
}

func TestHTTPResolverRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(make([]byte, (32<<20)+1))
	}))
	defer server.Close()
	resolver := &HTTPResolver{Client: server.Client()}
	_, err := resolver.get(context.Background(), server.URL, false)
	if err == nil || !strings.Contains(err.Error(), "exceeds 32 MiB") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPResolverReturnsBoundedHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte("upstream unavailable"))
	}))
	defer server.Close()
	resolver := &HTTPResolver{Client: server.Client()}
	_, err := resolver.get(context.Background(), server.URL, false)
	if err == nil || !strings.Contains(err.Error(), "502 Bad Gateway: upstream unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}
