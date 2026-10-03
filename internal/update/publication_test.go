package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequiredPublicationEvidenceMatchesSelectedTarget(t *testing.T) {
	for _, datasource := range []string{"go", "npm", "crates.io", "github-releases", "nuget"} {
		t.Run(datasource, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/@v/list"):
					fmt.Fprintln(w, "v1.1.0")
				case strings.HasSuffix(r.URL.Path, "/@latest"):
					fmt.Fprint(w, `{"Version":"v1.1.0"}`)
				case strings.HasSuffix(r.URL.Path, ".info"):
					fmt.Fprint(w, `{"Version":"v1.1.0","Time":"2026-01-01T00:00:00Z"}`)
				case strings.Contains(r.URL.Path, "/crates/"):
					fmt.Fprint(w, `{"versions":[{"num":"1.1.0","created_at":"2026-01-01T00:00:00Z"}]}`)
				case strings.HasSuffix(r.URL.Path, "/latest") && strings.HasPrefix(r.URL.Path, "/npm/"):
					fmt.Fprint(w, `{"version":"1.1.0"}`)
				case strings.HasPrefix(r.URL.Path, "/npm/"):
					fmt.Fprint(w, `{"time":{"1.1.0":"2026-01-01T00:00:00Z"}}`)
				case strings.HasSuffix(r.URL.Path, "/releases/latest"):
					fmt.Fprint(w, `{"tag_name":"v1.1.0"}`)
				case strings.Contains(r.URL.Path, "/git/ref/tags/"):
					fmt.Fprintf(w, `{"object":{"type":"commit","sha":%q}}`, strings.Repeat("a", 40))
				case strings.Contains(r.URL.Path, "/releases/tags/"):
					fmt.Fprint(w, `{"tag_name":"v1.1.0","published_at":"2026-01-01T00:00:00Z"}`)
				case strings.HasSuffix(r.URL.Path, "/index.json"):
					fmt.Fprint(w, `{"versions":["1.1.0"]}`)
				default:
					t.Fatalf("unexpected request %s", r.URL)
				}
			}))
			defer server.Close()
			resolver := NewHTTPResolver(server.Client(), "")
			resolver.GoProxy = server.URL
			resolver.NPMRegistry = server.URL + "/npm"
			resolver.CratesAPI = server.URL
			resolver.GitHubAPI = server.URL
			resolver.NuGetAPI = server.URL
			result, err := resolver.Resolve(context.Background(), Candidate{Datasource: datasource, Name: "example.test/package", NeedPublished: true}, false)
			if err != nil {
				t.Fatal(err)
			}
			if datasource == "nuget" {
				if result.PublishedAt != nil {
					t.Fatal("invented NuGet publication time")
				}
			} else if result.PublishedAt == nil || result.PublishedAt.Year() != 2026 {
				t.Fatalf("missing publication evidence %+v", result)
			}
		})
	}
}

func TestPublicationRejectsMismatchedGoTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".info") {
			fmt.Fprint(w, `{"Version":"v9.0.0","Time":"2026-01-01T00:00:00Z"}`)
		} else if strings.HasSuffix(r.URL.Path, "/@latest") {
			fmt.Fprint(w, `{"Version":"v1.1.0"}`)
		} else {
			fmt.Fprintln(w, "v1.1.0")
		}
	}))
	defer server.Close()
	resolver := NewHTTPResolver(server.Client(), "")
	resolver.GoProxy = server.URL
	if _, err := resolver.Resolve(context.Background(), Candidate{Datasource: "go", Name: "example.test/module", NeedPublished: true}, false); err == nil {
		t.Fatal("age evidence accepted for a different version")
	}
}
