// Package desktop gives a target a private graphical display the operator can
// watch from a browser.
//
// A tool that drives a real browser — a recorded replay, a test in headed mode,
// an agent clicking through a page — has nowhere to draw on a headless server,
// and what it would draw is exactly what the operator wants to see. This starts
// a virtual display with a VNC server and a noVNC web client on the target, all
// bound to the target's loopback. The control plane reaches the web client with
// a port forward, so nothing new listens on the target's network.
//
// A browser running inside that display sees the target's own localhost, which
// is also the simplest way to use a web app that only listens there.
package desktop

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Runner executes a shell script on one target and returns its stdout.
type Runner func(ctx context.Context, script string) (string, error)

// Desktop is one running display.
type Desktop struct {
	Dir     string // private state on the target: pids, logs, the browser profile
	Display int    // the X display number: tools run with DISPLAY=:<Display>
	Port    int    // the noVNC web client, on the target's loopback
	Width   int
	Height  int
}

// MissingError names what the target needs installed before it can do this.
type MissingError struct{ Tools []string }

func (e *MissingError) Error() string {
	return "this machine cannot host a live desktop; install: " + strings.Join(e.Tools, ", ")
}

var stateDir = regexp.MustCompile(`^/tmp/lectern-live-[A-Za-z0-9]{6}$`)

// reapScript stops desktops a previous server left running. Forwards live in
// memory, so after a restart nothing can reach those desktops and nothing would
// ever stop them. Each records the server instance that started it; any that
// names a different one is dead weight.
const reapScript = `
reap() {
  d="$1"
  for p in "$d"/*.pid; do
    [ -f "$p" ] || continue
    pid=$(cat "$p" 2>/dev/null)
    case "$pid" in ''|*[!0-9]*) continue;; esac
    kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null
  done
  sleep 0.3
  for p in "$d"/*.pid; do
    [ -f "$p" ] || continue
    pid=$(cat "$p" 2>/dev/null)
    case "$pid" in ''|*[!0-9]*) continue;; esac
    kill -KILL -- "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null
  done
  rm -rf -- "$d"
}
`

// Start brings up a display of the given size. owner identifies this server
// process, so a later server can tell its own desktops from abandoned ones.
func Start(ctx context.Context, run Runner, owner string, width, height int) (*Desktop, error) {
	if width < 640 || width > 3840 || height < 480 || height > 2160 {
		width, height = 1440, 900
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]{8,64}$`).MatchString(owner) {
		return nil, fmt.Errorf("invalid owner id")
	}
	script := "# lectern-desktop-start\n" + reapScript + fmt.Sprintf(`
W=%d; H=%d; OWNER=%s
for old in /tmp/lectern-live-??????; do
  [ -d "$old" ] && [ ! -L "$old" ] || continue
  [ "$(cat "$old/owner" 2>/dev/null)" = "$OWNER" ] || reap "$old"
done
missing=""
for b in Xvfb x11vnc websockify python3; do command -v "$b" >/dev/null 2>&1 || missing="$missing $b"; done
novnc=""
for d in /usr/share/novnc /usr/share/webapps/novnc /usr/share/noVNC /opt/novnc; do
  [ -f "$d/vnc.html" ] && { novnc="$d"; break; }
done
[ -n "$novnc" ] || missing="$missing novnc"
if [ -n "$missing" ]; then echo "MISSING$missing"; exit 0; fi
dir=$(mktemp -d /tmp/lectern-live-XXXXXX) || { echo "ERROR could not create state directory"; exit 0; }
chmod 700 "$dir"; printf %%s "$OWNER" >"$dir/owner"
n=""
for c in $(seq 90 139); do
  [ -e "/tmp/.X11-unix/X$c" ] || [ -e "/tmp/.X$c-lock" ] || { n=$c; break; }
done
[ -n "$n" ] || { echo "ERROR no free display number"; rm -rf "$dir"; exit 0; }
freeport() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
vport=$(freeport); wport=$(freeport)
# Every descriptor is redirected: a background process holding this script's
# stdout open would keep the caller waiting for as long as the desktop lives.
setsid nohup Xvfb ":$n" -screen 0 "${W}x${H}x24" -nolisten tcp >"$dir/xvfb.log" 2>&1 </dev/null &
echo $! >"$dir/xvfb.pid"
i=0; while [ ! -e "/tmp/.X11-unix/X$n" ] && [ $i -lt 60 ]; do sleep 0.1; i=$((i+1)); done
if [ ! -e "/tmp/.X11-unix/X$n" ]; then
  echo "ERROR the display did not start: $(tail -n 1 "$dir/xvfb.log" 2>/dev/null)"; reap "$dir"; exit 0
fi
setsid nohup x11vnc -display ":$n" -localhost -rfbport "$vport" -forever -shared -nopw -quiet -noxdamage >"$dir/x11vnc.log" 2>&1 </dev/null &
echo $! >"$dir/x11vnc.pid"
setsid nohup websockify --web "$novnc" "127.0.0.1:$wport" "127.0.0.1:$vport" >"$dir/websockify.log" 2>&1 </dev/null &
echo $! >"$dir/websockify.pid"
for wm in openbox fluxbox xfwm4 matchbox-window-manager; do
  if command -v "$wm" >/dev/null 2>&1; then
    DISPLAY=":$n" setsid nohup "$wm" >"$dir/wm.log" 2>&1 </dev/null &
    echo $! >"$dir/wm.pid"; break
  fi
done
up=0; i=0
while [ $i -lt 60 ]; do
  python3 -c 'import socket,sys;s=socket.socket();s.settimeout(0.3);sys.exit(s.connect_ex(("127.0.0.1",int(sys.argv[1]))))' "$wport" && { up=1; break; }
  sleep 0.1; i=$((i+1))
done
if [ $up != 1 ]; then
  echo "ERROR the web client did not start: $(tail -n 1 "$dir/websockify.log" 2>/dev/null)"; reap "$dir"; exit 0
fi
echo "OK $dir $n $wport"
`, width, height, owner)
	out, err := run(ctx, script)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) >= 1 && f[0] == "MISSING":
			return nil, &MissingError{Tools: f[1:]}
		case len(f) >= 1 && f[0] == "ERROR":
			return nil, fmt.Errorf("%s", strings.TrimSpace(strings.TrimPrefix(line, "ERROR")))
		case len(f) == 4 && f[0] == "OK" && stateDir.MatchString(f[1]):
			display, e1 := strconv.Atoi(f[2])
			port, e2 := strconv.Atoi(f[3])
			if e1 == nil && e2 == nil {
				return &Desktop{Dir: f[1], Display: display, Port: port, Width: width, Height: height}, nil
			}
		}
	}
	return nil, fmt.Errorf("the target did not report a desktop: %s", strings.TrimSpace(out))
}

// Stop ends a desktop and everything started inside it, and removes its state.
func Stop(ctx context.Context, run Runner, dir string) error {
	if !stateDir.MatchString(dir) {
		return fmt.Errorf("not a desktop state directory")
	}
	_, err := run(ctx, "# lectern-desktop-stop\n"+reapScript+
		fmt.Sprintf(`d=%s; [ -d "$d" ] && [ ! -L "$d" ] && reap "$d"; true`+"\n", shellQuote(dir)))
	return err
}

// OpenBrowser starts a browser inside the desktop at address and names the one
// it found. A target with no browser still has a working desktop: a tool that
// brings its own, as Playwright does, only needs DISPLAY.
func OpenBrowser(ctx context.Context, run Runner, d *Desktop, address string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("the address must be an http(s) URL")
	}
	if !stateDir.MatchString(d.Dir) {
		return "", fmt.Errorf("not a desktop state directory")
	}
	script := fmt.Sprintf(`# lectern-desktop-browser
dir=%s; url=%s; n=%d; W=%d; H=%d
[ -d "$dir" ] || { echo "ERROR the desktop is gone"; exit 0; }
bin=""
for b in chromium chromium-browser google-chrome google-chrome-stable; do
  command -v "$b" >/dev/null 2>&1 && { bin=$(command -v "$b"); break; }
done
if [ -z "$bin" ]; then
  bin=$(ls -1d "$HOME"/.cache/ms-playwright/chromium-*/chrome-linux*/chrome 2>/dev/null | sort | tail -n 1)
fi
if [ -n "$bin" ]; then
  sandbox=""; [ "$(id -u)" = 0 ] && sandbox="--no-sandbox"
  DISPLAY=":$n" setsid nohup "$bin" $sandbox --no-first-run --no-default-browser-check --disable-session-crashed-bubble \
    --user-data-dir="$dir/profile" --window-position=0,0 --window-size="$W,$H" "$url" >"$dir/browser.log" 2>&1 </dev/null &
  echo $! >"$dir/browser-$!.pid"; echo "BROWSER $bin"; exit 0
fi
if command -v firefox >/dev/null 2>&1; then
  DISPLAY=":$n" setsid nohup firefox --no-remote --profile "$dir/profile" --width "$W" --height "$H" "$url" >"$dir/browser.log" 2>&1 </dev/null &
  mkdir -p "$dir/profile"; echo $! >"$dir/browser-$!.pid"; echo "BROWSER firefox"; exit 0
fi
echo NOBROWSER
`, shellQuote(d.Dir), shellQuote(parsed.String()), d.Display, d.Width, d.Height)
	out, err := run(ctx, script)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "BROWSER "); ok {
			return name, nil
		}
		if msg, ok := strings.CutPrefix(strings.TrimSpace(line), "ERROR "); ok {
			return "", fmt.Errorf("%s", msg)
		}
	}
	return "", fmt.Errorf("no browser is installed on this machine; a tool that brings its own can still draw here with DISPLAY=:%d", d.Display)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
