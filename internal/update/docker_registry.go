package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Public Docker Hub tokens grant anonymous pull scope only. Their value never
// enters reports, configuration, saved plans, logs, or GitHub authorization.
func (r *HTTPResolver) dockerRegistryTags(ctx context.Context, name string) ([]string, error) {
	endpoint := "https://auth.docker.io/token?" + url.Values{"service": {"registry.docker.io"}, "scope": {"repository:" + name + ":pull"}}.Encode()
	body, err := r.get(ctx, endpoint, false)
	if err != nil {
		return nil, err
	}
	var auth struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &auth); err != nil {
		return nil, errors.New("invalid Docker Hub anonymous token response")
	}
	if auth.Token == "" {
		auth.Token = auth.AccessToken
	}
	if auth.Token == "" || strings.ContainsAny(auth.Token, "\r\n") {
		return nil, errors.New("Docker Hub returned no usable anonymous pull token")
	}
	return r.registryTags(ctx, "https://registry-1.docker.io/v2/"+name+"/tags/list?n=1000", name, auth.Token)
}

func (r *HTTPResolver) registryTags(ctx context.Context, endpoint, name, token string) ([]string, error) {
	if r.Client == nil {
		return nil, errors.New("HTTP client is required")
	}
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Host == "" || origin.User != nil {
		return nil, errors.New("invalid registry endpoint")
	}
	client := *r.Client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.Scheme != origin.Scheme || !strings.EqualFold(req.URL.Host, origin.Host) || req.URL.User != nil {
			return errors.New("Docker registry redirect leaves its origin")
		}
		if r.Client.CheckRedirect != nil {
			return r.Client.CheckRedirect(req, via)
		}
		return nil
	}
	seen := map[string]bool{}
	var tags []string
	totalBytes := 0
	for page := 0; endpoint != ""; page++ {
		if page >= 100 || seen[endpoint] {
			return nil, errors.New("Docker registry pagination exceeds limit or repeats a page")
		}
		seen[endpoint] = true
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(request)
		if err != nil {
			return nil, errors.New("Docker registry request failed")
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Docker registry returned HTTP %d", response.StatusCode)
		}
		totalBytes += len(body)
		if totalBytes > 32<<20 {
			return nil, errors.New("Docker registry response exceeds 32 MiB")
		}
		var batch struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, err
		}
		if batch.Name != name {
			return nil, errors.New("Docker registry returned a different repository")
		}
		tags = append(tags, batch.Tags...)
		endpoint = ""
		header := response.Header.Get("Link")
		link := githubNextLink(header)
		if header != "" && link == "" {
			return nil, errors.New("unrecognized Docker registry pagination link")
		}
		if link != "" {
			next, err := url.Parse(link)
			if err != nil {
				return nil, errors.New("invalid Docker registry pagination link")
			}
			next = origin.ResolveReference(next)
			if next.Scheme != origin.Scheme || !strings.EqualFold(next.Host, origin.Host) || next.Path != origin.Path || next.User != nil || next.Fragment != "" {
				return nil, errors.New("Docker registry pagination leaves its repository")
			}
			endpoint = next.String()
		}
	}
	return tags, nil
}
