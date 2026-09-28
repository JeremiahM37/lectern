package console

import (
	"strings"
	"testing"
)

// A tab in the help text renders as up to eight columns, so one line starts
// visibly further right than its neighbours. Every line is indented with
// spaces instead.
func TestDashboardHelpHasNoTabs(t *testing.T) {
	m := sampleDashboard()
	for _, context := range []string{"Sessions", "Approvals", "Projects", "Tasks", "Review"} {
		m.helpContext = context
		for i, line := range m.helpLines() {
			if strings.Contains(line, "\t") {
				t.Errorf("%s help line %d contains a tab: %q", context, i+1, line)
			}
		}
	}
}
