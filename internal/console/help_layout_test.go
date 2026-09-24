package console

import (
	"strings"
	"testing"
)

// A tab in the help text renders as up to eight columns, so one line starts
// visibly further right than its neighbours. Every line is indented with one
// space instead.
func TestDashboardHelpHasNoTabs(t *testing.T) {
	for i, line := range strings.Split(dashboardHelp, "\n") {
		if strings.Contains(line, "\t") {
			t.Errorf("help line %d contains a tab: %q", i+1, line)
		}
	}
}
