package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
)

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
		return chooseVersion([]string{latest.Version}, false)
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
	versions := make([]string, 0, len(response.Versions)+1)
	for version := range response.Versions {
		versions = append(versions, version)
	}
	if latest := response.DistTags["latest"]; latest != "" {
		versions = append(versions, latest)
	}
	return chooseVersion(versions, true)
}
