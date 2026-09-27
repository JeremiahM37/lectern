package browser

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Runner executes a shell script on one machine and returns its stdout.
type Runner func(ctx context.Context, script string) (string, error)

// Process is one headless browser started by Launch.
type Process struct {
	Dir    string // private state: pid, log, profile
	Port   int    // DevTools, on that machine's loopback
	Path   string // the browser-level DevTools WebSocket path
	Binary string
}

// ErrNoBrowser means the machine has no Chromium-family browser to start.
var ErrNoBrowser = errors.New("no Chromium or Chrome is installed on this machine " +
	"(install chromium, or run `npx playwright install chromium`)")

var browserDir = regexp.MustCompile(`^/tmp/lectern-browser-[A-Za-z0-9]{6}$`)

// findScript prints the first Chromium-family binary it finds, including the
// one Playwright downloads, so a machine set up for browser tests needs nothing
// more.
const findScript = `
bin=""
for b in chromium chromium-browser google-chrome google-chrome-stable chrome; do
  command -v "$b" >/dev/null 2>&1 && { bin=$(command -v "$b"); break; }
done
if [ -z "$bin" ]; then
  bin=$(ls -1d "$HOME"/.cache/ms-playwright/chromium-*/chrome-linux*/chrome 2>/dev/null | sort -V | tail -n 1)
fi
if [ -z "$bin" ]; then
  bin=$(ls -1d "$HOME"/.cache/ms-playwright/chromium_headless_shell-*/chrome-linux*/*headless_shell 2>/dev/null | sort -V | tail -n 1)
fi
`

const reapScript = `
reap() {
  d="$1"
  pid=$(cat "$d/browser.pid" 2>/dev/null)
  case "$pid" in ''|*[!0-9]*) ;; *) kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null; sleep 0.3; kill -KILL -- "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null;; esac
  rm -rf -- "$d"
}
`

// LaunchOptions shape one browser.
type LaunchOptions struct {
	Width, Height int
	// ProxyPort, when set, sends every request — loopback included — through
	// an HTTP proxy on 127.0.0.1 of the machine running the browser. It is how
	// a browser on the control plane still sees a target's localhost.
	ProxyPort int
}

// Launch starts a headless browser. owner identifies this server process, so a
// later server can tell its own browsers from ones an earlier one abandoned.
func Launch(ctx context.Context, run Runner, owner string, opts LaunchOptions) (*Process, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9]{8,64}$`).MatchString(owner) {
		return nil, fmt.Errorf("invalid owner id")
	}
	if opts.Width < 320 || opts.Width > 3840 || opts.Height < 320 || opts.Height > 2160 {
		opts.Width, opts.Height = 1280, 800
	}
	proxy := ""
	if opts.ProxyPort > 0 && opts.ProxyPort < 65536 {
		// <-loopback> undoes Chromium's implicit bypass for localhost, which
		// is exactly the traffic that has to reach the target.
		proxy = fmt.Sprintf("--proxy-server=http://127.0.0.1:%d '--proxy-bypass-list=<-loopback>'", opts.ProxyPort)
	}
	script := "# lectern-browser-start\n" + reapScript + findScript + fmt.Sprintf(`
OWNER=%s; W=%d; H=%d
for old in /tmp/lectern-browser-??????; do
  [ -d "$old" ] && [ ! -L "$old" ] || continue
  [ "$(cat "$old/owner" 2>/dev/null)" = "$OWNER" ] || reap "$old"
done
[ -n "$bin" ] || { echo NOBROWSER; exit 0; }
dir=$(mktemp -d /tmp/lectern-browser-XXXXXX) || { echo "ERROR could not create state directory"; exit 0; }
chmod 700 "$dir"; printf %%s "$OWNER" >"$dir/owner"
sandbox=""; [ "$(id -u)" = 0 ] && sandbox="--no-sandbox"
headless="--headless=new"; case "$bin" in *headless_shell) headless="";; esac
setsid nohup "$bin" $headless $sandbox --remote-debugging-address=127.0.0.1 --remote-debugging-port=0 \
  --user-data-dir="$dir/profile" --no-first-run --no-default-browser-check --disable-extensions \
  --disable-background-networking --disable-sync --mute-audio --hide-scrollbars \
  --window-size="$W,$H" %s about:blank >"$dir/browser.log" 2>&1 </dev/null &
echo $! >"$dir/browser.pid"
i=0
while [ ! -s "$dir/profile/DevToolsActivePort" ] && [ $i -lt 150 ]; do sleep 0.1; i=$((i+1)); done
if [ ! -s "$dir/profile/DevToolsActivePort" ]; then
  echo "ERROR the browser did not start: $(tail -n 2 "$dir/browser.log" 2>/dev/null | tr '\n' ' ')"; reap "$dir"; exit 0
fi
port=$(sed -n 1p "$dir/profile/DevToolsActivePort"); path=$(sed -n 2p "$dir/profile/DevToolsActivePort")
echo "OK $dir $port $path $bin"
`, owner, opts.Width, opts.Height, proxy)
	out, err := run(ctx, script)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 1 && f[0] == "NOBROWSER":
			return nil, ErrNoBrowser
		case len(f) >= 1 && f[0] == "ERROR":
			return nil, fmt.Errorf("%s", strings.TrimSpace(strings.TrimPrefix(line, "ERROR")))
		case len(f) >= 5 && f[0] == "OK" && browserDir.MatchString(f[1]) && strings.HasPrefix(f[3], "/devtools/browser/"):
			port, err := strconv.Atoi(f[2])
			if err == nil && port > 0 && port < 65536 {
				return &Process{Dir: f[1], Port: port, Path: f[3], Binary: strings.Join(f[4:], " ")}, nil
			}
		}
	}
	return nil, fmt.Errorf("the machine did not report a browser: %s", strings.TrimSpace(out))
}

// Stop ends a browser and removes its profile.
func Stop(ctx context.Context, run Runner, dir string) error {
	if !browserDir.MatchString(dir) {
		return fmt.Errorf("not a browser state directory")
	}
	_, err := run(ctx, "# lectern-browser-stop\n"+reapScript+
		fmt.Sprintf(`d=%s; [ -d "$d" ] && [ ! -L "$d" ] && reap "$d"; true`+"\n", shellQuote(dir)))
	return err
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
