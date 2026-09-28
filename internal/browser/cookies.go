package browser

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/helpers"
)

//go:embed cookies.py
var cookiesScript string

// CookieResult is what an import reports: counts only, never a cookie.
type CookieResult struct {
	Imported int            `json:"imported"`
	Skipped  map[string]int `json:"skipped"`
	Sites    int            `json:"sites"`
}

// ImportCookies loads cookies into a running browser from its own machine:
// kind "file" reads (and then deletes) a cookies file at source, in Netscape
// cookies.txt or JSON form; kind "chrome" reads a local Chrome, Chromium,
// Brave or Edge profile directory ("auto" finds one). domains, when not
// empty, limits the import to those sites. The work happens in a script on
// that machine, which talks to the browser over its loopback DevTools port,
// so cookie values never pass through Lectern.
func ImportCookies(ctx context.Context, run Runner, proc *Process, kind, source string, domains []string) (CookieResult, error) {
	return ImportCookiesWith(ctx, run, "", proc, kind, source, domains)
}

// ImportCookiesWith is ImportCookies on a machine whose lectern binary is at
// lectern ("" for none): a cookies file is then read by `lectern helper
// browser-cookies`. A Chrome profile always needs the Python script, which
// reads the profile's SQLite database.
func ImportCookiesWith(ctx context.Context, run Runner, lectern string, proc *Process, kind, source string, domains []string) (CookieResult, error) {
	if kind != "file" && kind != "chrome" {
		return CookieResult{}, fmt.Errorf("import from a cookies file or a Chrome profile")
	}
	if kind == "file" && (!path.IsAbs(source) || !strings.HasPrefix(source, proc.Dir+"/")) {
		return CookieResult{}, fmt.Errorf("a cookies file must be staged in the browser's own directory")
	}
	if kind == "chrome" && source != "auto" && !path.IsAbs(source) && !strings.HasPrefix(source, "~/") {
		return CookieResult{}, fmt.Errorf("give the profile directory as an absolute path, ~/…, or auto")
	}
	for _, d := range domains {
		if strings.ContainsAny(d, ", \t\n") || len(d) > 253 {
			return CookieResult{}, fmt.Errorf("invalid domain %q", d)
		}
	}
	script := fmt.Sprintf("# lectern-browser-cookies\ncommand -v python3 >/dev/null 2>&1 || { echo '{\"error\":\"python3 is required\"}'; exit 0; }\n"+
		"python3 -c %s %s %s %s %s %s 2>&1 | tail -n 1\n", shellQuote(cookiesScript), strconv.Itoa(proc.Port),
		shellQuote(proc.Path), kind, shellQuote(source), shellQuote(strings.Join(domains, ",")))
	if kind == "file" && lectern != "" {
		script = "# lectern-browser-cookies\n" + helpers.Invocation(lectern, "browser-cookies",
			[]string{strconv.Itoa(proc.Port), proc.Path, kind, source, strings.Join(domains, ",")}) + " 2>&1 | tail -n 1\n"
	}
	out, err := run(ctx, script)
	if err != nil {
		return CookieResult{}, err
	}
	line := strings.TrimSpace(out)
	var res struct {
		CookieResult
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(line), &res) != nil {
		return CookieResult{}, fmt.Errorf("the import failed: %s", clip(line, 300))
	}
	if res.Error != "" {
		return CookieResult{}, fmt.Errorf("%s", res.Error)
	}
	if res.Skipped == nil {
		res.Skipped = map[string]int{}
	}
	return res.CookieResult, nil
}
