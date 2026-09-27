package autonomy

import (
	"fmt"
	"regexp"
	"strings"
)

// Requirement is an observation, never permission to execute a remediation.
// Identities, providers, paths and proof are supplied by the controller/runner.
type Requirement struct {
	Capability    string   `json:"capability"`
	SchemaVersion int      `json:"schema_version"`
	Requirements  []string `json:"requirements,omitempty"`
	Imports       []string `json:"imports,omitempty"`
	Modules       []string `json:"modules,omitempty"`
	Binaries      []string `json:"binaries,omitempty"`
	PackageJSON   string   `json:"package_json,omitempty"`
	PackageLock   string   `json:"package_lock,omitempty"`
	Condition     string   `json:"condition"`
	Evidence      []string `json:"evidence"`
}

var requirementCapability = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func ValidateRequirements(rs []Requirement) error {
	if len(rs) > 4 {
		return fmt.Errorf("at most four prerequisite requirements permitted")
	}
	for _, r := range rs {
		if !requirementCapability.MatchString(r.Capability) || r.SchemaVersion != 1 || strings.TrimSpace(r.Condition) == "" || len(r.Condition) > 1000 || len(r.Requirements) > 32 || len(r.Imports) > 32 || len(r.Modules) > 32 || len(r.Binaries) > 32 || len(r.PackageJSON) > 512 || len(r.PackageLock) > 512 || len(r.Evidence) == 0 || len(r.Evidence) > 8 {
			return fmt.Errorf("invalid typed prerequisite requirement")
		}
		for _, group := range [][]string{r.Requirements, r.Imports, r.Modules, r.Binaries, r.Evidence} {
			for _, v := range group {
				if strings.TrimSpace(v) == "" || len(v) > 2000 {
					return fmt.Errorf("invalid prerequisite input or evidence")
				}
			}
		}
	}
	return nil
}
