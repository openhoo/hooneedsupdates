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
		// Only actionable targets have a meaningful update-type selection. Keep
		// current and incomplete members so selection cannot hide policy blocks.
		if entry.Status == "outdated" && len(allowedTypes) > 0 && !allowedTypes[entry.UpdateType] {
			return false
		}
		return true
	})
}
