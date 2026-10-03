package update

import (
	"context"
	"encoding/json"
	"net/url"
)

func (r *HTTPResolver) resolveCrate(ctx context.Context, name string, includePrereleases bool) (Resolution, error) {
	body, err := r.get(ctx, r.endpoint(r.CratesAPI, "crates/"+url.PathEscape(name)), false)
	if err != nil {
		return Resolution{}, err
	}
	var response struct {
		Versions []struct {
			Number string `json:"num"`
			Yanked bool   `json:"yanked"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return Resolution{}, err
	}
	versions := make([]string, 0, len(response.Versions))
	for _, version := range response.Versions {
		if !version.Yanked {
			versions = append(versions, version.Number)
		}
	}
	return chooseVersion(versions, includePrereleases)
}
