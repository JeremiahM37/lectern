package localruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// The runtime is started once and then reused, so it keeps the PATH of the
// shell that first started it. An agent installed afterwards (into a
// directory that shell did not have on PATH, like ~/.local/bin after a fresh
// install) stayed invisible: "not on PATH" on every later `lectern up` until
// the runtime was stopped by hand. Commands now hand the runtime their PATH,
// and it adds any directories it was missing. It only ever adds, so a
// command run with a thin PATH (cron, an IDE task) cannot take any away.

const pathRoute = "/__lectern_local/path"

var pathMu sync.Mutex

// mergePath appends the directories of extra that current lacks, keeping
// current's order in front.
func mergePath(current, extra string) (string, bool) {
	sep := string(os.PathListSeparator)
	have := map[string]bool{}
	var dirs []string
	// Windows paths are case-insensitive and may end in a separator, so
	// C:\Tools\ and c:\tools name one directory there.
	key := func(d string) string {
		if runtime.GOOS == "windows" {
			return strings.ToLower(strings.TrimRight(d, `\/`))
		}
		return d
	}
	for _, d := range strings.Split(current, sep) {
		if d != "" && !have[key(d)] {
			have[key(d)] = true
			dirs = append(dirs, d)
		}
	}
	changed := false
	for _, d := range strings.Split(extra, sep) {
		if d == "" || have[key(d)] || !filepath.IsAbs(d) {
			continue
		}
		have[key(d)] = true
		dirs = append(dirs, d)
		changed = true
	}
	return strings.Join(dirs, sep), changed
}

func pathHandler(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !equal(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), token) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var in struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		pathMu.Lock()
		merged, changed := mergePath(os.Getenv("PATH"), in.Path)
		if changed {
			_ = os.Setenv("PATH", merged)
			// Sessions start inside the runtime's private tmux server, which
			// keeps the environment it was started with.
			if tmux, err := exec.LookPath("tmux"); err == nil {
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
				_ = exec.CommandContext(ctx, tmux, "set-environment", "-g", "PATH", merged).Run()
				cancel()
			}
		}
		pathMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"changed": changed})
	}
}

// SharePath hands the runtime this process's PATH. It reports whether the
// runtime gained any directories. An older runtime without the route is
// left alone.
func SharePath(ctx context.Context, ep Endpoint) (bool, error) {
	body, _ := json.Marshal(map[string]string{"path": os.Getenv("PATH")})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL+pathRoute, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return false, nil
	}
	var out struct {
		Changed bool `json:"changed"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&out) != nil {
		return false, fmt.Errorf("share PATH: %s", res.Status)
	}
	return out.Changed, nil
}
