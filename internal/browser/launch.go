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

// ProfileName is what a persistent profile may be called.
var ProfileName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

var browserDir = regexp.MustCompile(`^/tmp/lectern-browser-[A-Za-z0-9]{6}$`)

// findScript prints the first Chromium-family binary it finds, including the
// one Playwright downloads, so a machine set up for browser tests needs nothing
// more.
const findScript = `
bin=""
# LECTERN_BROWSER_BIN, in the machine's own environment, picks the browser.
if [ -n "${LECTERN_BROWSER_BIN:-}" ] && [ -x "$LECTERN_BROWSER_BIN" ]; then bin="$LECTERN_BROWSER_BIN"; fi
[ -n "$bin" ] || for b in chromium chromium-browser google-chrome google-chrome-stable chrome; do
  command -v "$b" >/dev/null 2>&1 && { bin=$(command -v "$b"); break; }
done
if [ -z "$bin" ]; then
  bin=$(ls -1d "$HOME"/.cache/ms-playwright/chromium-*/chrome-linux*/chrome 2>/dev/null | sort -V | tail -n 1)
fi
if [ -z "$bin" ]; then
  bin=$(ls -1d "$HOME"/.cache/ms-playwright/chromium_headless_shell-*/chrome-*/*headless[_-]shell 2>/dev/null | sort -V | tail -n 1)
fi
`

const reapScript = `
reap() {
  d="$1"
  pid=$(cat "$d/browser.pid" 2>/dev/null)
  case "$pid" in ''|*[!0-9]*) ;; *)
    kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null
    # Give it a moment to write its profile before it is killed.
    i=0; while [ $i -lt 30 ] && kill -0 "$pid" 2>/dev/null; do sleep 0.1; i=$((i+1)); done
    kill -KILL -- "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null;; esac
  rm -rf -- "$d"
}
`

// Find names the browser Launch would start on a machine, and its version,
// or returns ErrNoBrowser.
func Find(ctx context.Context, run Runner) (string, string, error) {
	out, err := run(ctx, "# lectern-browser-find\n"+findScript+`[ -n "$bin" ] || { echo NOBROWSER; exit 0; }
printf 'BIN %s\n' "$bin"; printf 'VERSION %s\n' "$("$bin" --version 2>/dev/null | head -n 1)"
`)
	if err != nil {
		return "", "", err
	}
	var bin, version string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.TrimSpace(line) == "NOBROWSER":
			return "", "", ErrNoBrowser
		case strings.HasPrefix(line, "BIN "):
			bin = strings.TrimSpace(line[4:])
		case strings.HasPrefix(line, "VERSION "):
			version = strings.TrimSpace(line[8:])
		}
	}
	if bin == "" {
		return "", "", ErrNoBrowser
	}
	return bin, version, nil
}

// LaunchOptions shape one browser.
type LaunchOptions struct {
	Width, Height int
	// Profile names a persistent profile under ~/.lectern/browser-profiles on
	// the browser's machine; cookies and storage in it survive the browser.
	// "" is a throwaway profile removed with the browser.
	Profile string
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
	if opts.Profile != "" && !ProfileName.MatchString(opts.Profile) {
		return nil, fmt.Errorf("invalid profile name")
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
OWNER=%s; W=%d; H=%d; PROFILE=%s
for old in /tmp/lectern-browser-??????; do
  [ -d "$old" ] && [ ! -L "$old" ] || continue
  [ "$(cat "$old/owner" 2>/dev/null)" = "$OWNER" ] && continue
  # Another server's browser stays while that server keeps it alive: two
  # Lecterns can share a machine. One not touched for 5 minutes is abandoned.
  if [ -n "$(find "$old/alive" -mmin -5 2>/dev/null)" ]; then continue; fi
  reap "$old"
done
[ -n "$bin" ] || { echo NOBROWSER; exit 0; }
dir=$(mktemp -d /tmp/lectern-browser-XXXXXX) || { echo "ERROR could not create state directory"; exit 0; }
chmod 700 "$dir"; printf %%s "$OWNER" >"$dir/owner"; : >"$dir/alive"
profile="$dir/profile"
if [ -n "$PROFILE" ]; then
  profile="$HOME/.lectern/browser-profiles/$PROFILE"
  mkdir -p "$HOME/.lectern/browser-profiles" && chmod 700 "$HOME/.lectern" "$HOME/.lectern/browser-profiles" 2>/dev/null
  mkdir -p -m 700 "$profile" || { echo "ERROR could not create the profile directory"; reap "$dir"; exit 0; }
  # Chromium's own lock, and ours: the headless shell takes none.
  lpid=$(cat "$profile/lectern.lock" 2>/dev/null)
  case "$lpid" in ''|*[!0-9]*) ;; *)
    if kill -0 "$lpid" 2>/dev/null && tr '\0' ' ' <"/proc/$lpid/cmdline" 2>/dev/null | grep -qF -- "--user-data-dir=$profile "; then
      echo "ERROR profile $PROFILE is in use by another browser"; reap "$dir"; exit 0
    fi;; esac
  if [ -L "$profile/SingletonLock" ]; then
    lockpid=$(readlink "$profile/SingletonLock" | sed 's/.*-//')
    if [ -n "$lockpid" ] && kill -0 "$lockpid" 2>/dev/null; then
      echo "ERROR profile $PROFILE is in use by another browser"; reap "$dir"; exit 0
    fi
  fi
  rm -f "$profile/DevToolsActivePort"
  printf %%s "$profile" >"$dir/profile-path"
fi
sandbox=""; [ "$(id -u)" = 0 ] && sandbox="--no-sandbox"
headless="--headless=new"; case "$bin" in *headless_shell|*headless-shell) headless="";; esac
# A server has no desktop session: no session bus, no keyring, often no
# system bus. The browser is kept off all of them, so it never waits on one,
# and its profile is always encrypted the same way (the basic store, which
# cookie import reads too), whatever the machine has.
start() {
  DBUS_SESSION_BUS_ADDRESS=disabled: setsid nohup "$bin" $headless $sandbox --remote-debugging-address=127.0.0.1 --remote-debugging-port=0 \
    --user-data-dir="$profile" --no-first-run --no-default-browser-check --disable-extensions \
    --disable-background-networking --disable-component-update --disable-default-apps \
    --disable-background-timer-throttling --disable-backgrounding-occluded-windows --disable-renderer-backgrounding \
    --password-store=basic --use-mock-keychain --disable-sync --mute-audio --hide-scrollbars \
    --window-size="$W,$H" %s about:blank >"$dir/browser.log" 2>&1 </dev/null &
  echo $! >"$dir/browser.pid"
  [ "$profile" = "$dir/profile" ] || echo $! >"$profile/lectern.lock"
  i=0
  while [ ! -s "$profile/DevToolsActivePort" ] && [ $i -lt 150 ]; do
    sleep 0.1; i=$((i+1))
    kill -0 "$(cat "$dir/browser.pid")" 2>/dev/null || break
  done
}
start
# Inside a container without user namespaces Chromium's own sandbox cannot
# start. The browser still runs as this same unprivileged user.
if [ ! -s "$profile/DevToolsActivePort" ] && [ -z "$sandbox" ] && grep -qi sandbox "$dir/browser.log" 2>/dev/null; then
  sandbox="--no-sandbox"; start
fi
if [ ! -s "$profile/DevToolsActivePort" ]; then
  echo "ERROR the browser did not start: $(tail -n 2 "$dir/browser.log" 2>/dev/null | tr '\n' ' ')"; reap "$dir"; exit 0
fi
port=$(sed -n 1p "$profile/DevToolsActivePort"); path=$(sed -n 2p "$profile/DevToolsActivePort")
echo "OK $dir $port $path $bin"
`, owner, opts.Width, opts.Height, shellQuote(opts.Profile), proxy)
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

// KeepAlive tells other servers on the machine this browser is still in use;
// Launch reaps one whose owner stopped doing this.
func KeepAlive(ctx context.Context, run Runner, dirs ...string) error {
	var b strings.Builder
	b.WriteString("# lectern-browser-alive\n")
	for _, d := range dirs {
		if browserDir.MatchString(d) {
			fmt.Fprintf(&b, "[ -d %[1]s ] && touch %[1]s/alive\n", shellQuote(d))
		}
	}
	b.WriteString("true\n")
	_, err := run(ctx, b.String())
	return err
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
