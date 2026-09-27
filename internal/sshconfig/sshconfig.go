// Package sshconfig reads the control plane's ~/.ssh/config so its hosts can
// become Lectern targets in one click (docs/ssh.md).
//
// The host list comes from the file itself (Host lines, following Include).
// What each host resolves to comes from `ssh -G <alias>` when OpenSSH is
// installed, so Match blocks, wildcards and defaults resolve exactly as ssh
// would resolve them; without it, a small first-match-wins reader of the
// same file stands in.
package sshconfig

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Host is one concrete alias and what it resolves to.
type Host struct {
	Alias         string   `json:"alias"`
	HostName      string   `json:"hostname"`
	User          string   `json:"user"`
	Port          int      `json:"port"`
	IdentityFiles []string `json:"identity_files,omitempty"`
	ProxyJump     string   `json:"proxy_jump,omitempty"`
	ProxyCommand  bool     `json:"proxy_command,omitempty"`
	ForwardAgent  bool     `json:"forward_agent,omitempty"`
	GSSAPI        bool     `json:"gssapi,omitempty"`
	IdentityAgent string   `json:"identity_agent,omitempty"`
	// SecurityKey is set when an identity file is a FIDO2 (sk-) key.
	SecurityKey bool `json:"security_key,omitempty"`
	// Resolver says how the values were found: "ssh -G" or "file".
	Resolver string `json:"resolver"`
}

// DefaultPath is ~/.ssh/config for the user Lectern runs as.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", "config")
}

type directive struct{ key, value string }

type block struct {
	patterns []string
	lines    []directive
}

// parse reads a config file and its Includes into Host blocks, in order.
// Lines before the first Host apply to every host (pattern "*").
func parse(path string, depth int, out *[]block) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cur := &block{patterns: []string{"*"}}
	flush := func() {
		if len(cur.lines) > 0 || cur.patterns[0] != "*" {
			*out = append(*out, *cur)
		}
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value := splitLine(line)
		switch strings.ToLower(key) {
		case "host":
			flush()
			cur = &block{patterns: strings.Fields(value)}
		case "match":
			// A Match block's conditions cannot be judged here; its lines are
			// left to ssh -G. Collect them under a pattern nothing matches.
			flush()
			cur = &block{patterns: []string{"!*"}}
		case "include":
			if depth > 8 {
				continue
			}
			for _, pat := range strings.Fields(value) {
				if strings.HasPrefix(pat, "~/") {
					home, _ := os.UserHomeDir()
					pat = filepath.Join(home, pat[2:])
				} else if !filepath.IsAbs(pat) {
					pat = filepath.Join(filepath.Dir(DefaultPath()), pat)
				}
				matches, _ := filepath.Glob(pat)
				for _, m := range matches {
					flush()
					cur = &block{patterns: []string{"*"}}
					_ = parse(m, depth+1, out)
				}
			}
		default:
			cur.lines = append(cur.lines, directive{strings.ToLower(key), value})
		}
	}
	flush()
	return sc.Err()
}

func splitLine(line string) (string, string) {
	key, value, ok := strings.Cut(line, "=")
	if !ok || strings.ContainsAny(key, " \t") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return "", ""
		}
		return fields[0], strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	}
	return strings.TrimSpace(key), strings.Trim(strings.TrimSpace(value), `"`)
}

// Aliases lists every concrete Host name in the file: no wildcards, no
// negations, each once, in file order.
func Aliases(path string) ([]string, error) {
	var blocks []block
	if err := parse(path, 0, &blocks); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, b := range blocks {
		for _, p := range b.patterns {
			if strings.ContainsAny(p, "*?!") || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// matches is ssh_config pattern matching for one host name.
func matches(patterns []string, host string) bool {
	hit := false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		ok, _ := filepath.Match(strings.TrimPrefix(p, "!"), host)
		if ok && neg {
			return false
		}
		if ok {
			hit = true
		}
	}
	return hit
}

// fromFile resolves an alias with the first-match-wins rule, without ssh.
func fromFile(path, alias string) Host {
	var blocks []block
	_ = parse(path, 0, &blocks)
	h := Host{Alias: alias, Resolver: "file"}
	set := map[string]bool{}
	for _, b := range blocks {
		if !matches(b.patterns, alias) {
			continue
		}
		for _, d := range b.lines {
			if d.key == "identityfile" {
				h.IdentityFiles = append(h.IdentityFiles, d.value)
				continue
			}
			if set[d.key] {
				continue
			}
			set[d.key] = true
			apply(&h, d.key, d.value)
		}
	}
	if h.HostName == "" {
		h.HostName = alias
	}
	if h.Port == 0 {
		h.Port = 22
	}
	return h
}

func apply(h *Host, key, value string) {
	switch key {
	case "hostname":
		h.HostName = value
	case "user":
		h.User = value
	case "port":
		h.Port, _ = strconv.Atoi(value)
	case "proxyjump":
		if !strings.EqualFold(value, "none") {
			h.ProxyJump = value
		}
	case "proxycommand":
		h.ProxyCommand = !strings.EqualFold(value, "none")
	case "forwardagent":
		h.ForwardAgent = strings.EqualFold(value, "yes")
	case "gssapiauthentication":
		h.GSSAPI = strings.EqualFold(value, "yes")
	case "identityagent":
		if !strings.EqualFold(value, "none") && value != "SSH_AUTH_SOCK" {
			h.IdentityAgent = value
		}
	}
}

// Resolve reports what alias connects to, through `ssh -G` when available.
func Resolve(ctx context.Context, path, alias string) Host {
	if _, err := exec.LookPath("ssh"); err == nil {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		args := []string{"-G"}
		if path != DefaultPath() {
			args = append(args, "-F", path)
		}
		out, err := exec.CommandContext(cctx, "ssh", append(args, "--", alias)...).Output()
		if err == nil {
			h := Host{Alias: alias, Resolver: "ssh -G"}
			for _, line := range strings.Split(string(out), "\n") {
				key, value, _ := strings.Cut(strings.TrimSpace(line), " ")
				if key == "identityfile" {
					if p := expand(value); fileExists(p) {
						h.IdentityFiles = append(h.IdentityFiles, value)
					}
					continue
				}
				apply(&h, key, value)
			}
			markSecurityKey(&h)
			return h
		}
	}
	h := fromFile(path, alias)
	markSecurityKey(&h)
	return h
}

func markSecurityKey(h *Host) {
	for _, f := range h.IdentityFiles {
		if pub, err := os.ReadFile(expand(f) + ".pub"); err == nil && strings.HasPrefix(string(pub), "sk-") {
			h.SecurityKey = true
		}
	}
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// List resolves every alias in the file.
func List(ctx context.Context, path string) ([]Host, error) {
	aliases, err := Aliases(path)
	if err != nil {
		return nil, err
	}
	out := make([]Host, 0, len(aliases))
	for _, a := range aliases {
		out = append(out, Resolve(ctx, path, a))
	}
	return out, nil
}
