package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/console"
	"github.com/JeremiahM37/lectern/v2/internal/pluginpkg"
)

const pluginUsage = `usage:
  lectern plugin list
  lectern plugin search [QUERY] [--refresh]
  lectern plugin install PATH|GIT_URL|ID [--ref REF] [--commit SHA] [--path SUBDIR] [--source NAME] [--project ID]... [--yes]
  lectern plugin update ID [--ref REF] [--yes]
  lectern plugin trust ID [--yes]
  lectern plugin enable ID [--project ID]... [--global]
  lectern plugin disable ID
  lectern plugin remove ID
  lectern plugin secret ID NAME            (reads the value from stdin; empty removes it)
  lectern plugin source add NAME GIT_URL [--ref REF] | source list | source remove NAME
  lectern plugin new ID [DIR]
  lectern plugin validate [DIR]`

// pluginOffline handles the subcommands that need no server: scaffolding a
// plugin and validating one. ok is false for every other subcommand.
func pluginOffline(args []string, out io.Writer) (ok bool, err error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "new":
		if len(args) < 2 || len(args) > 3 {
			return true, fmt.Errorf("usage: lectern plugin new ID [DIR]")
		}
		dir := args[1]
		if len(args) == 3 {
			dir = args[2]
		}
		files, err := pluginpkg.Scaffold(args[1])
		if err != nil {
			return true, err
		}
		if _, err := os.Stat(dir); err == nil {
			return true, fmt.Errorf("%s already exists", dir)
		}
		for _, f := range files {
			p := filepath.Join(dir, filepath.FromSlash(f.Path))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return true, err
			}
			if err := os.WriteFile(p, f.Data, os.FileMode(f.Mode)); err != nil {
				return true, err
			}
		}
		fmt.Fprintf(out, "created %s\nnext: edit %s, then `lectern plugin validate %s`\n", dir, filepath.Join(dir, pluginpkg.ManifestName), dir)
		return true, nil
	case "validate":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		files, err := pluginpkg.ReadDir(dir)
		if err != nil {
			return true, err
		}
		pkg, err := pluginpkg.Load(files, false)
		if err != nil {
			return true, err
		}
		m := pkg.Manifest
		fmt.Fprintf(out, "%s %s — valid (%s format, %d files, sha256 %s)\n", m.ID, m.Version, pkg.Format, len(files), pkg.Hash[:16])
		printCapabilities(out, m.CapabilityList())
		if len(pkg.Skipped) > 0 {
			fmt.Fprintf(out, "not imported: %s\n", strings.Join(pkg.Skipped, ", "))
		}
		return true, nil
	}
	return false, nil
}

func printCapabilities(out io.Writer, caps []pluginpkg.Capability) {
	if len(caps) == 0 {
		fmt.Fprintln(out, "capabilities: none")
		return
	}
	fmt.Fprintln(out, "capabilities:")
	for _, c := range caps {
		fmt.Fprintf(out, "  %s\n", c.Key)
		for _, d := range c.Detail {
			fmt.Fprintf(out, "      %s\n", d)
		}
	}
}

type pluginFlags struct {
	ref, commit, subdir, source string
	projects                    []int64
	yes, refresh, global        bool
	rest                        []string
}

func parsePluginFlags(args []string) (pluginFlags, error) {
	var f pluginFlags
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			i++
			return args[i], nil
		}
		var err error
		switch a {
		case "--ref":
			f.ref, err = next()
		case "--commit":
			f.commit, err = next()
		case "--path":
			f.subdir, err = next()
		case "--source":
			f.source, err = next()
		case "--project":
			var v string
			if v, err = next(); err == nil {
				n, perr := strconv.ParseInt(v, 10, 64)
				if perr != nil {
					return f, fmt.Errorf("--project takes a project id")
				}
				f.projects = append(f.projects, n)
			}
		case "--yes", "-y":
			f.yes = true
		case "--refresh":
			f.refresh = true
		case "--global":
			f.global = true
		default:
			if strings.HasPrefix(a, "--") {
				return f, fmt.Errorf("unknown option %s\n%s", a, pluginUsage)
			}
			f.rest = append(f.rest, a)
		}
		if err != nil {
			return f, err
		}
	}
	return f, nil
}

type pluginPreview struct {
	Hash     string `json:"hash"`
	Format   string `json:"format"`
	Commit   string `json:"commit"`
	Manifest struct {
		ID, Name, Version, Description string
	} `json:"manifest"`
	Capabilities  []pluginpkg.Capability `json:"capabilities"`
	Accept        []string               `json:"accept"`
	Contributions map[string]int         `json:"contributions"`
	Skipped       []string               `json:"skipped"`
	FromVersion   string                 `json:"from_version"`
	Grown         []string               `json:"grown"`
	Unchanged     bool                   `json:"unchanged"`
}

// consentFlow shows a preview and, after the person says yes, consents to
// exactly the capabilities it showed. The server refuses the consent unless
// the caller is a signed-in person, whatever this prompt says.
func consentFlow(c *console.Client, raw []byte, f pluginFlags, out io.Writer, in io.Reader) ([]byte, error) {
	var pv pluginPreview
	if err := json.Unmarshal(raw, &pv); err != nil {
		return nil, err
	}
	m := pv.Manifest
	if pv.FromVersion != "" {
		fmt.Fprintf(out, "%s: %s → %s\n", m.ID, pv.FromVersion, m.Version)
	} else {
		fmt.Fprintf(out, "%s %s — %s\n", m.ID, m.Version, m.Name)
	}
	if m.Description != "" {
		fmt.Fprintf(out, "  %s\n", m.Description)
	}
	if pv.Commit != "" {
		fmt.Fprintf(out, "commit %s\n", pv.Commit)
	}
	fmt.Fprintf(out, "content sha256 %s\n", pv.Hash)
	if pv.Unchanged {
		fmt.Fprintln(out, "already installed at exactly this content")
		return nil, nil
	}
	var kinds []string
	for k, n := range pv.Contributions {
		kinds = append(kinds, fmt.Sprintf("%s %d", k, n))
	}
	sort.Strings(kinds)
	fmt.Fprintf(out, "contributes: %s\n", strings.Join(kinds, ", "))
	printCapabilities(out, pv.Capabilities)
	if len(pv.Skipped) > 0 {
		fmt.Fprintf(out, "not imported from this Claude Code plugin: %s\n", strings.Join(pv.Skipped, ", "))
	}
	if len(pv.Grown) > 0 {
		fmt.Fprintln(out, "NEW capabilities in this version:")
		for _, g := range pv.Grown {
			fmt.Fprintf(out, "  + %s\n", g)
		}
	}
	if !f.yes {
		fmt.Fprint(out, "Allow these capabilities and install? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return nil, fmt.Errorf("not installed")
		}
	}
	body := map[string]any{"hash": pv.Hash, "accept": pv.Accept}
	if len(f.projects) > 0 {
		body["project_ids"] = f.projects
	}
	raw, err := c.JSON("POST", "/api/plugins/install", body)
	if err != nil {
		return nil, err
	}
	var done struct{ ID, Version, Status string }
	_ = json.Unmarshal(raw, &done)
	return []byte(fmt.Sprintf("installed %s %s (%s)\n", done.ID, done.Version, done.Status)), nil
}

func pluginCommand(c *console.Client, args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("%s", pluginUsage)
	}
	op := args[0]
	f, err := parsePluginFlags(args[1:])
	if err != nil {
		return nil, err
	}
	need := func(n int) error {
		if len(f.rest) != n {
			return fmt.Errorf("%s", pluginUsage)
		}
		return nil
	}
	switch op {
	case "list":
		raw, err := c.JSON("GET", "/api/plugins", nil)
		if err != nil {
			return nil, err
		}
		return formatPluginList(raw)
	case "search":
		q := url.Values{}
		if len(f.rest) > 0 {
			q.Set("q", strings.Join(f.rest, " "))
		}
		if f.refresh {
			q.Set("refresh", "1")
		}
		return c.JSON("GET", "/api/plugins/search?"+q.Encode(), nil)
	case "install":
		if err := need(1); err != nil {
			return nil, err
		}
		target := f.rest[0]
		src := map[string]any{}
		if st, statErr := os.Stat(target); statErr == nil && st.IsDir() {
			abs, _ := filepath.Abs(target)
			src["kind"], src["path"] = "path", abs
		} else if strings.Contains(target, "://") || strings.Contains(target, "@") && strings.Contains(target, ":") {
			src["kind"], src["url"], src["ref"], src["commit"], src["subdir"] = "git", target, f.ref, f.commit, f.subdir
		} else {
			src["kind"], src["id"], src["index"] = "index", target, f.source
		}
		raw, err := c.JSON("POST", "/api/plugins/preview", src)
		if err != nil {
			return nil, err
		}
		return consentFlow(c, raw, f, os.Stdout, os.Stdin)
	case "update", "trust":
		if err := need(1); err != nil {
			return nil, err
		}
		body := map[string]any{}
		if op == "update" && f.ref != "" {
			body["ref"] = f.ref
		}
		raw, err := c.JSON("POST", "/api/plugins/"+url.PathEscape(f.rest[0])+"/"+op, body)
		if err != nil {
			return nil, err
		}
		return consentFlow(c, raw, f, os.Stdout, os.Stdin)
	case "enable", "disable":
		if err := need(1); err != nil {
			return nil, err
		}
		body := map[string]any{"enabled": op == "enable"}
		if len(f.projects) > 0 {
			body["project_ids"] = f.projects
		} else if f.global {
			body["project_ids"] = []int64{}
		}
		return c.JSON("PUT", "/api/plugins/"+url.PathEscape(f.rest[0]), body)
	case "remove":
		if err := need(1); err != nil {
			return nil, err
		}
		return c.JSON("DELETE", "/api/plugins/"+url.PathEscape(f.rest[0]), nil)
	case "secret":
		if err := need(2); err != nil {
			return nil, err
		}
		if interactiveTerminal() {
			fmt.Fprintf(os.Stdout, "value for %s (empty removes it): ", f.rest[1])
		}
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		return c.JSON("PUT", "/api/plugins/"+url.PathEscape(f.rest[0])+"/secrets", map[string]string{f.rest[1]: strings.TrimRight(line, "\r\n")})
	case "source":
		if len(f.rest) == 0 {
			return nil, fmt.Errorf("%s", pluginUsage)
		}
		switch f.rest[0] {
		case "list":
			return c.JSON("GET", "/api/plugin-sources", nil)
		case "add":
			if len(f.rest) != 3 {
				return nil, fmt.Errorf("usage: lectern plugin source add NAME GIT_URL [--ref REF]")
			}
			return c.JSON("POST", "/api/plugin-sources", map[string]any{"name": f.rest[1], "url": f.rest[2], "ref": f.ref})
		case "remove":
			if len(f.rest) != 2 {
				return nil, fmt.Errorf("usage: lectern plugin source remove NAME")
			}
			return c.JSON("DELETE", "/api/plugin-sources/"+url.PathEscape(f.rest[1]), nil)
		}
	}
	return nil, fmt.Errorf("unknown plugin operation %q\n%s", op, pluginUsage)
}

func formatPluginList(raw []byte) ([]byte, error) {
	var doc struct {
		Plugins []struct {
			ID         string  `json:"id"`
			Version    string  `json:"version"`
			Status     string  `json:"status"`
			Bundled    bool    `json:"bundled"`
			ProjectIDs []int64 `json:"project_ids"`
			Problem    string  `json:"problem"`
		} `json:"plugins"`
		Local struct {
			Agents []string `json:"agents"`
			MCP    []struct {
				Project string   `json:"project"`
				Servers []string `json:"servers"`
			} `json:"mcp_servers"`
		} `json:"local"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, p := range doc.Plugins {
		scope := "everywhere"
		if len(p.ProjectIDs) > 0 {
			var ids []string
			for _, id := range p.ProjectIDs {
				ids = append(ids, strconv.FormatInt(id, 10))
			}
			scope = "projects " + strings.Join(ids, ",")
		}
		origin := ""
		if p.Bundled {
			origin = " (bundled)"
		}
		fmt.Fprintf(&b, "%-28s %-12s %-14s %s%s\n", p.ID, p.Version, p.Status, scope, origin)
		if p.Problem != "" {
			fmt.Fprintf(&b, "    %s\n", p.Problem)
		}
	}
	if len(doc.Local.Agents) > 0 {
		fmt.Fprintf(&b, "local agents: %s\n", strings.Join(doc.Local.Agents, ", "))
	}
	for _, m := range doc.Local.MCP {
		fmt.Fprintf(&b, "local MCP servers (%s): %s\n", m.Project, strings.Join(m.Servers, ", "))
	}
	return []byte(b.String()), nil
}
