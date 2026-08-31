package automation

import (
	"regexp"

	"github.com/openhoo/hooneedsupdates/internal/config"
	"github.com/openhoo/hooneedsupdates/internal/update"
)

func SelectReport(report update.Report, selection config.Selection) update.Report {
	allowedTypes := stringSet(selection.UpdateTypes)
	allowedManagers := stringSet(selection.Managers)
	patterns := make([]*regexp.Regexp, 0, len(selection.Dependencies))
	for _, pattern := range selection.Dependencies {
		patterns = append(patterns, regexp.MustCompile(pattern))
	}
	return update.FilterReport(report, func(entry update.Update) bool {
		if len(allowedManagers) > 0 && !allowedManagers[string(entry.Manager)] {
			return false
		}
		if len(patterns) > 0 && !matchesAny(patterns, entry.Name) {
			return false
		}
		// Resolution failures have no trustworthy update type yet. Keep matching
		// entries so a type filter can never hide an unresolved selected input.
		if entry.Status != "unresolved" && len(allowedTypes) > 0 && !allowedTypes[entry.UpdateType] {
			return false
		}
		return true
	})
}
