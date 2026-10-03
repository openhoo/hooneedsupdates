package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

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
	if !includePrereleases {
		body, requestErr := r.get(ctx, r.endpoint(r.GitHubAPI, "repos/"+name+"/releases/latest"), true)
		if requestErr != nil {
			var status *datasourceHTTPError
			if !errors.As(requestErr, &status) || status.status != http.StatusNotFound {
				return "", requestErr
			}
		} else {
			var release struct {
				Tag string `json:"tag_name"`
			}
			if decodeErr := json.Unmarshal(body, &release); decodeErr != nil {
				return "", decodeErr
			}
			if release.Tag != "" {
				chosen, err := chooseVersion([]string{release.Tag}, false)
				return chosen.Version, err
			}
		}
	}
	endpoint := r.endpoint(r.GitHubAPI, "repos/"+name+"/tags")
	var versions []string
	const pageSize = 100
	const pageLimit = 100
	for page := 1; page <= pageLimit; page++ {
		pageURL := endpoint + "?per_page=" + strconv.Itoa(pageSize) + "&page=" + strconv.Itoa(page)
		body, headers, tagsErr := r.getWithHeaders(ctx, pageURL, true)
		if tagsErr != nil {
			return "", tagsErr
		}
		var tags []struct {
			Name string `json:"name"`
		}
		if decodeErr := json.Unmarshal(body, &tags); decodeErr != nil {
			return "", decodeErr
		}
		for _, entry := range tags {
			versions = append(versions, entry.Name)
		}
		if len(tags) < pageSize || githubNextLink(headers.Get("Link")) == "" {
			break
		}
		if page == pageLimit {
			return "", errors.New("GitHub tags pagination exceeds page limit")
		}
	}
	chosen, chooseErr := chooseVersion(versions, includePrereleases)
	return chosen.Version, chooseErr
}

func githubNextLink(link string) string {
	for _, part := range strings.Split(link, ",") {
		fields := strings.Split(part, ";")
		if len(fields) < 2 {
			continue
		}
		target := strings.TrimSpace(fields[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, parameter := range fields[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if !ok || !strings.EqualFold(key, "rel") {
				continue
			}
			for _, relation := range strings.Fields(strings.Trim(value, `"`)) {
				if relation == "next" {
					return strings.Trim(target, "<>")
				}
			}
		}
	}
	return ""
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
