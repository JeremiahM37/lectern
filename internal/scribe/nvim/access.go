package nvim

import (
	"fmt"
	"strings"
	"sync"
)

// Access is the agent's access to the editor, the "Agent editor access"
// setting: always, only while the editor is open, or never.
type Access int

const (
	AccessOn       Access = iota // "on": the tool is available (default)
	AccessWhenOpen               // "when-open": only while the editor is open
	AccessOff                    // "off": the tool is disabled
)

var (
	accessMu  sync.Mutex
	accessNow = AccessOn
)

// GetAccess is the current agent editor access.
func GetAccess() Access {
	accessMu.Lock()
	defer accessMu.Unlock()
	return accessNow
}

// SetAccess changes the agent editor access.
func SetAccess(a Access) {
	accessMu.Lock()
	defer accessMu.Unlock()
	accessNow = a
}

// ParseAccess reads a setting value. It accepts the names the settings
// screen uses and their aliases.
func ParseAccess(s string) (Access, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "always":
		return AccessOn, nil
	case "when-open", "open", "when_open":
		return AccessWhenOpen, nil
	case "off", "never":
		return AccessOff, nil
	}
	return AccessOn, fmt.Errorf("unknown editor access %q (want on, when-open or off)", s)
}

// String is the setting's name, as ParseAccess reads it.
func (a Access) String() string {
	switch a {
	case AccessWhenOpen:
		return "when-open"
	case AccessOff:
		return "off"
	}
	return "on"
}

// Label is the setting's name as the settings screen shows it.
func (a Access) Label() string {
	switch a {
	case AccessWhenOpen:
		return "When editor open"
	case AccessOff:
		return "Never"
	}
	return "Always"
}
