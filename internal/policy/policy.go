// Package policy holds a project's "always allow" rules, evaluated server-side
// before a human is ever paged.
//
// Rules live in projects.policy_json:
//
//	{"allow": [{"tool": "Bash", "prefix": "pytest"}, {"tool": "Edit"}]}
//
// Bash rules match on the command's FIRST TOKEN (or an explicit glob); other
// tools match wholesale. Token matching, not substring: an auto-approver that
// substring-matched once denied `terraform version` because it contains "rm ".
package policy

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// Rule is one always-allow entry.
type Rule struct {
	Tool   string `json:"tool"`
	Prefix string `json:"prefix,omitempty"`
	Glob   string `json:"glob,omitempty"`
}

// Policy is a project's rule set.
type Policy struct {
	Allow []Rule `json:"allow"`
}

// Parse decodes policy_json, treating anything unparseable as "no rules".
func Parse(raw string) Policy {
	var p Policy
	if strings.TrimSpace(raw) == "" {
		return p
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Policy{}
	}
	return p
}

// PatternFor derives the rule an operator means when they say "always allow this".
func PatternFor(toolName string, input map[string]any) Rule {
	if toolName == "Bash" {
		cmd := strings.TrimSpace(asString(input["command"]))
		prefix := ""
		if fields := strings.Fields(cmd); len(fields) > 0 {
			prefix = fields[0]
		}
		return Rule{Tool: "Bash", Prefix: prefix}
	}
	return Rule{Tool: toolName}
}

// wrapperCommands run whatever follows them, so their first token says
// nothing about what executes: a rule for "sudo" or "bash" would allow every
// command at all.
var wrapperCommands = map[string]bool{
	"sudo": true, "doas": true, "su": true, "env": true, "exec": true, "eval": true,
	"bash": true, "sh": true, "zsh": true, "dash": true, "fish": true,
	"xargs": true, "nohup": true, "timeout": true, "nice": true, "time": true, "command": true,
	"python": true, "python3": true, "node": true, "perl": true, "ruby": true,
}

// BroadenableForSession reports whether "allow for the rest of this session"
// is a sensible scope for this call. For Bash it is the command's first
// token; a wrapper, or a path-qualified or empty command, would make that
// rule far broader than the one command the person saw, so it is refused.
func BroadenableForSession(toolName string, input map[string]any) bool {
	if toolName != "Bash" {
		return true
	}
	fields := strings.Fields(strings.TrimSpace(asString(input["command"])))
	if len(fields) == 0 {
		return false
	}
	first := fields[0]
	return !wrapperCommands[first] && !strings.ContainsAny(first, "/=$`(")
}

// Matches reports whether a policy already permits this call.
func Matches(p Policy, toolName string, input map[string]any) bool {
	for _, rule := range p.Allow {
		if rule.Tool != toolName {
			continue
		}
		if toolName != "Bash" {
			return true
		}
		cmd := strings.TrimSpace(asString(input["command"]))
		fields := strings.Fields(cmd)
		if rule.Prefix != "" && len(fields) > 0 && fields[0] == rule.Prefix {
			return true
		}
		if rule.Glob != "" {
			if ok, err := filepath.Match(rule.Glob, cmd); err == nil && ok {
				return true
			}
		}
	}
	return false
}

// AddRule appends a rule unless an identical one is already present.
func AddRule(p Policy, r Rule) Policy {
	for _, existing := range p.Allow {
		if existing == r {
			return p
		}
	}
	p.Allow = append(p.Allow, r)
	return p
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
