package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var dockerFrom = regexp.MustCompile(`(?im)^\s*FROM(?:\s+--platform=[^\s]+)?\s+([^\s]+)(?:\s+AS\s+([^\s]+))?`)
var imageDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validImageDigest(value string) bool { return imageDigest.MatchString(value) }

func dockerRepository(name string) (string, error) {
	for _, alias := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		name = strings.TrimPrefix(name, alias)
	}
	first := strings.Split(name, "/")[0]
	if name == "" || strings.ContainsAny(name, "$:@\\?#") || strings.Contains(first, ".") || first == "localhost" || strings.Count(name, "/") > 1 {
		return "", fmt.Errorf("registry or image reference %s is unsupported", name)
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*(/[a-z0-9][a-z0-9_.-]*)?$`).MatchString(name) {
		return "", errors.New("invalid Docker Hub image name")
	}
	if !strings.Contains(name, "/") {
		name = "library/" + name
	}
	return name, nil
}

func extractDocker(rel string, data []byte) []Candidate {
	var result []Candidate
	stages := map[string]bool{}
	for _, match := range dockerFrom.FindAllSubmatchIndex(data, -1) {
		start, end := match[2], match[3]
		token := string(data[start:end])
		if len(match) > 5 && match[4] >= 0 {
			stages[strings.ToLower(string(data[match[4]:match[5]]))] = true
		}
		if token == "scratch" || stages[strings.ToLower(token)] {
			continue
		}
		name, tag, digest := token, "", ""
		if at := strings.Index(name, "@"); at >= 0 {
			digest = name[at+1:]
			name = name[:at]
		}
		if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
			tag = name[colon+1:]
			start += colon + 1
			name = name[:colon]
		}
		entry := candidate(ManagerDocker, "docker", name, tag, rel, data, byteRange{start, end})
		entry.CurrentDigest = digest
		if _, err := dockerRepository(name); err != nil {
			entry.UnsupportedReason = err.Error()
		}
		if tag == "" || normalizeVersion(tag) == "" {
			entry.UnsupportedReason = "Docker reference requires an explicit version tag"
		}
		if digest != "" && !validImageDigest(digest) {
			entry.UnsupportedReason = "Docker pin requires a valid SHA256 digest"
		}
		result = append(result, entry)
	}
	return result
}

func (r *HTTPResolver) resolveDockerCandidate(ctx context.Context, entry Candidate, prereleases bool) (Resolution, error) {
	result, err := r.resolveDocker(ctx, entry.Name, entry.CurrentVersion, prereleases)
	if err != nil {
		return Resolution{}, err
	}
	if entry.CurrentDigest == "" && !entry.NeedPublished {
		return result, nil
	}
	name, err := dockerRepository(entry.Name)
	if err != nil {
		return Resolution{}, err
	}
	body, err := r.get(ctx, r.endpoint(r.DockerHub, "repositories/"+name+"/tags/"+url.PathEscape(result.Version)), false)
	if err != nil {
		return Resolution{}, err
	}
	var metadata struct {
		Name    string    `json:"name"`
		Digest  string    `json:"digest"`
		Updated time.Time `json:"last_updated"`
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		return Resolution{}, err
	}
	if metadata.Name != result.Version || !validImageDigest(metadata.Digest) {
		return Resolution{}, errors.New("Docker Hub returned invalid tag digest metadata")
	}
	result.Digest = metadata.Digest
	if !metadata.Updated.IsZero() {
		result.PublishedAt = &metadata.Updated
	}
	return result, nil
}

func (r *HTTPResolver) resolveDocker(ctx context.Context, name, current string, includePrereleases bool) (Resolution, error) {
	var err error
	name, err = dockerRepository(name)
	if err != nil {
		return Resolution{}, err
	}
	endpoint := r.endpoint(r.DockerHub, "repositories/"+name+"/tags?page_size=100")
	tags, err := r.dockerTags(ctx, endpoint)
	var status *datasourceHTTPError
	if errors.As(err, &status) && status.status == 403 && strings.Contains(status.message, "pagination offset too large") && r.DockerHub == "https://hub.docker.com/v2" {
		tags, err = r.dockerRegistryTags(ctx, name)
	}
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
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Host == "" {
		return nil, errors.New("invalid Docker Hub endpoint")
	}
	seen := map[string]bool{}
	const pageLimit = 100
	for page := 0; endpoint != ""; page++ {
		if page >= pageLimit {
			return nil, errors.New("Docker tags pagination exceeds page limit")
		}
		if seen[endpoint] {
			return nil, errors.New("Docker tags pagination contains a cycle")
		}
		seen[endpoint] = true
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
		endpoint = ""
		if response.Next != "" {
			next, err := url.Parse(response.Next)
			if err != nil {
				return nil, errors.New("invalid Docker tags pagination URL")
			}
			next = origin.ResolveReference(next)
			if next.Scheme != origin.Scheme || !strings.EqualFold(next.Host, origin.Host) || next.User != nil || next.Fragment != "" {
				return nil, errors.New("Docker tags pagination leaves the configured registry")
			}
			endpoint = next.String()
		}
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
