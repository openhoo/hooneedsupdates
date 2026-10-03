package update

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

func (r *HTTPResolver) resolveNuGet(ctx context.Context, name string, includePrereleases bool) (Resolution, error) {
	endpoint := r.endpoint(r.NuGetAPI, strings.ToLower(url.PathEscape(name))+"/index.json")
	body, err := r.get(ctx, endpoint, false)
	if err != nil {
		return Resolution{}, err
	}
	var response struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return Resolution{}, err
	}
	return chooseVersion(response.Versions, includePrereleases)
}
