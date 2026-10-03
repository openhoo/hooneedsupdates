package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"golang.org/x/mod/module"
)

// Publication metadata is requested only when policy requires it. NuGet has no
// publication timestamp in the supported flat-container API and blocks age rules.
func (r *HTTPResolver) Resolve(ctx context.Context, entry Candidate, prereleases bool) (Resolution, error) {
	result, err := r.resolveVersion(ctx, entry, prereleases)
	if err != nil || !entry.NeedPublished || result.PublishedAt != nil {
		return result, err
	}
	var endpoint string
	switch entry.Datasource {
	case "go":
		name, escapeErr := module.EscapePath(entry.Name)
		if escapeErr != nil {
			return Resolution{}, escapeErr
		}
		version, escapeErr := module.EscapeVersion(result.Version)
		if escapeErr != nil {
			return Resolution{}, escapeErr
		}
		endpoint = r.endpoint(r.GoProxy, name+"/@v/"+version+".info")
	case "npm":
		endpoint = r.endpoint(r.NPMRegistry, url.PathEscape(entry.Name))
	case "crates.io":
		endpoint = r.endpoint(r.CratesAPI, "crates/"+url.PathEscape(entry.Name))
	case "github-releases":
		endpoint = r.endpoint(r.GitHubAPI, "repos/"+entry.Name+"/releases/tags/"+url.PathEscape(result.Version))
	default:
		return result, nil
	}
	body, err := r.get(ctx, endpoint, entry.Datasource == "github-releases")
	if err != nil {
		return Resolution{}, err
	}
	var response struct {
		Version   string          `json:"Version"`
		Tag       string          `json:"tag_name"`
		Time      json.RawMessage `json:"time"`
		Published time.Time       `json:"published_at"`
		Versions  []struct {
			Number  string    `json:"num"`
			Created time.Time `json:"created_at"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return Resolution{}, err
	}
	if (entry.Datasource == "go" && response.Version != result.Version) || (entry.Datasource == "github-releases" && response.Tag != result.Version) {
		return Resolution{}, errors.New("publication metadata does not match the selected target")
	}
	published := response.Published
	switch entry.Datasource {
	case "go":
		if len(response.Time) > 0 {
			if err := json.Unmarshal(response.Time, &published); err != nil {
				return Resolution{}, err
			}
		}
	case "npm":
		var times map[string]time.Time
		if len(response.Time) > 0 {
			if err := json.Unmarshal(response.Time, &times); err != nil {
				return Resolution{}, err
			}
			published = times[result.Version]
		}
	case "crates.io":
		for _, v := range response.Versions {
			if strings.EqualFold(v.Number, result.Version) {
				published = v.Created
				break
			}
		}
	}
	if !published.IsZero() {
		result.PublishedAt = &published
	}
	return result, nil
}
