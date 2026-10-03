package update

import (
	"context"
	"fmt"
	"github.com/openhoo/hooneedsupdates/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDockerDigestPinAndUnsupportedInventory(t *testing.T) {
	pin := "sha256:" + strings.Repeat("a", 64)
	data := []byte("FROM --platform=$BUILDPLATFORM docker.io/library/alpine:3.24.0@" + pin + " AS base # comment\nFROM base AS build\nFROM scratch\nFROM ghcr.io/org/image:1.0.0\nFROM alpine@" + pin + "\nFROM ${IMAGE}\n")
	entries := extractDocker("Dockerfile", data)
	if len(entries) != 4 {
		t.Fatalf("inventory: %+v", entries)
	}
	first := entries[0]
	if first.CurrentDigest != pin || first.UnsupportedReason != "" || first.CurrentValue != "3.24.0@"+pin || string(data[first.Start:first.End]) != first.CurrentValue {
		t.Fatalf("bad pinned span: %+v", first)
	}
	for _, entry := range entries[1:] {
		if entry.UnsupportedReason == "" {
			t.Fatalf("unsupported reference invisible: %+v", entry)
		}
	}
	next := "sha256:" + strings.Repeat("b", 64)
	result := classifyResolved(config.Default(), first, Resolution{Version: "3.24.0", Digest: next})
	if result.Status != "outdated" {
		t.Fatalf("same-tag digest change lost: %+v", result)
	}
	replacement := replacementFor(result)
	if replacement != "3.24.0@"+next {
		t.Fatalf("replacement %q", replacement)
	}
}
func TestDockerPinnedResolverRequiresTagDigest(t *testing.T) {
	for _, digest := range []string{"sha256:" + strings.Repeat("b", 64), "", "sha256:invalid"} {
		t.Run(fmt.Sprint(len(digest)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repositories/library/alpine/tags" {
					fmt.Fprint(w, `{"results":[{"name":"3.24.1"}]}`)
				} else {
					fmt.Fprintf(w, `{"name":"3.24.1","digest":%q,"last_updated":"2026-06-16T08:24:29Z"}`, digest)
				}
			}))
			defer server.Close()
			resolver := NewHTTPResolver(server.Client(), "")
			resolver.DockerHub = server.URL
			result, err := resolver.Resolve(context.Background(), Candidate{Datasource: "docker", Name: "alpine", CurrentVersion: "3.24.0", CurrentDigest: "sha256:" + strings.Repeat("a", 64)}, false)
			if validImageDigest(digest) {
				if err != nil || result.Digest != digest || result.PublishedAt == nil {
					t.Fatalf("resolution %+v: %v", result, err)
				}
			} else if err == nil {
				t.Fatal("invalid digest accepted")
			}
		})
	}
}
