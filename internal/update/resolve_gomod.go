package update

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	"strings"
)

func (r *HTTPResolver) resolveGo(ctx context.Context, name string, includePrereleases bool) (Resolution, error) {
	escaped, err := module.EscapePath(name)
	if err != nil {
		return Resolution{}, err
	}
	body, listErr := r.get(ctx, r.endpoint(r.GoProxy, escaped+"/@v/list"), false)
	versions := strings.Fields(string(body))
	latestBody, latestErr := r.get(ctx, r.endpoint(r.GoProxy, escaped+"/@latest"), false)
	if latestErr == nil {
		var latest struct {
			Version string `json:"Version"`
		}
		if err := json.Unmarshal(latestBody, &latest); err != nil {
			return Resolution{}, err
		}
		if latest.Version != "" {
			versions = append(versions, latest.Version)
		}
	}
	if len(versions) == 0 {
		if listErr != nil {
			return Resolution{}, listErr
		}
		return Resolution{}, latestErr
	}
	return chooseGoVersion(versions, includePrereleases)
}

func chooseGoVersion(versions []string, includePrereleases bool) (Resolution, error) {
	var chosen string
	for _, version := range versions {
		if !semver.IsValid(version) {
			continue
		}
		if !includePrereleases && semver.Prerelease(version) != "" && !module.IsPseudoVersion(version) {
			continue
		}
		if chosen == "" || semver.Compare(version, chosen) > 0 {
			chosen = version
		}
	}
	if chosen == "" {
		return Resolution{}, errors.New("no compatible Go module version found")
	}
	return Resolution{Version: chosen}, nil
}
