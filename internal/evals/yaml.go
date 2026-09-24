// Package evals holds the pieces of the agent-test-suite ("evals") feature
// that do not need the database or a live target: the `.lectern/evals/*.yaml`
// suite format and its parsing/validation, and pure result-aggregation math
// (leaderboards, run comparison) that the API layer feeds real rows into.
//
// See docs/evals.md for the on-disk YAML format this package reads.
package evals

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAMLCase is one case as written in a suite file. Field names match
// store.EvalCase's JSON tags so a hand-written case and one round-tripped
// through the API look the same.
type YAMLCase struct {
	Name         string `yaml:"name"`
	Prompt       string `yaml:"prompt"`
	BaseRef      string `yaml:"base_ref"`
	CheckCommand string `yaml:"check_command"`
	TimeoutS     int    `yaml:"timeout_s"`
	SetupCommand string `yaml:"setup_command"`
}

// YAMLSuite is a whole `.lectern/evals/*.yaml` file.
type YAMLSuite struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Cases       []YAMLCase `yaml:"cases"`
}

// ParseSuite parses and validates one suite file's bytes. It never returns a
// suite with an empty name or a case missing its own name/prompt — the same
// two fields the UI form requires, so a YAML suite cannot end up looking
// different from a hand-built one.
func ParseSuite(data []byte) (*YAMLSuite, error) {
	var s YAMLSuite
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return nil, fmt.Errorf("suite needs a name")
	}
	if len(s.Cases) == 0 {
		return nil, fmt.Errorf("suite %q has no cases", s.Name)
	}
	for i, c := range s.Cases {
		if strings.TrimSpace(c.Name) == "" {
			return nil, fmt.Errorf("case %d in suite %q has no name", i+1, s.Name)
		}
		if strings.TrimSpace(c.Prompt) == "" {
			return nil, fmt.Errorf("case %q has no prompt", c.Name)
		}
	}
	return &s, nil
}
