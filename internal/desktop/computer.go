package desktop

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
)

// Computer use: an agent sees a live desktop by screenshot and acts on it with
// xdotool, the way a person at that screen would. Whether an agent may do this
// at all is decided by the caller (docs/browser.md, "Computer use"); this file
// only knows how.

// Screenshot captures the whole display as PNG, with whichever capture tool
// the machine has.
func Screenshot(ctx context.Context, run Runner, d *Desktop) ([]byte, error) {
	if !stateDir.MatchString(d.Dir) {
		return nil, fmt.Errorf("not a desktop state directory")
	}
	script := fmt.Sprintf(`# lectern-desktop-screenshot
dir=%s; export DISPLAY=:%d; W=%d; H=%d
[ -d "$dir" ] || { echo "ERROR the desktop is gone"; exit 0; }
f="$dir/shot.png"; rm -f "$f"
if command -v import >/dev/null 2>&1; then import -window root "png:$f" 2>/dev/null
elif command -v scrot >/dev/null 2>&1; then scrot -o "$f" 2>/dev/null || scrot "$f" 2>/dev/null
elif command -v ffmpeg >/dev/null 2>&1; then ffmpeg -loglevel error -f x11grab -video_size "${W}x${H}" -i "$DISPLAY" -frames:v 1 -y "$f" 2>/dev/null
elif command -v xwd >/dev/null 2>&1 && command -v convert >/dev/null 2>&1; then xwd -root -silent | convert xwd:- "png:$f" 2>/dev/null
else echo "MISSING scrot"; exit 0; fi
[ -s "$f" ] || { echo "ERROR the screen could not be captured"; exit 0; }
printf 'PNG '; base64 -w0 "$f" 2>/dev/null || base64 "$f" | tr -d '\n'; echo; rm -f "$f"
`, shellQuote(d.Dir), d.Display, d.Width, d.Height)
	out, err := run(ctx, script)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "PNG "):
			return base64.StdEncoding.DecodeString(strings.TrimSpace(line[4:]))
		case strings.HasPrefix(line, "MISSING"):
			return nil, &MissingError{Tools: strings.Fields(line)[1:]}
		case strings.HasPrefix(line, "ERROR "):
			return nil, fmt.Errorf("%s", strings.TrimPrefix(line, "ERROR "))
		}
	}
	return nil, fmt.Errorf("the machine did not return a screenshot")
}

// Action is one input to a desktop.
type Action struct {
	Type   string `json:"action"` // click|double_click|right_click|move|type|key|scroll
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Text   string `json:"text"`
	Key    string `json:"key"`
	Scroll string `json:"direction"` // up|down|left|right
	Amount int    `json:"amount"`
}

// xdotool key names: letters, digits and names such as ctrl+shift+Tab.
var keyName = regexp.MustCompile(`^[A-Za-z0-9_]+(\+[A-Za-z0-9_]+)*$`)

// Act performs one action with xdotool.
func Act(ctx context.Context, run Runner, d *Desktop, a Action) error {
	if !stateDir.MatchString(d.Dir) {
		return fmt.Errorf("not a desktop state directory")
	}
	inside := func() error {
		if a.X < 0 || a.Y < 0 || a.X >= d.Width || a.Y >= d.Height {
			return fmt.Errorf("(%d,%d) is outside the %dx%d screen", a.X, a.Y, d.Width, d.Height)
		}
		return nil
	}
	var cmd string
	switch a.Type {
	case "click", "double_click", "right_click", "move":
		if err := inside(); err != nil {
			return err
		}
		cmd = fmt.Sprintf("xdotool mousemove --sync %d %d", a.X, a.Y)
		switch a.Type {
		case "click":
			cmd += " click 1"
		case "double_click":
			cmd += " click --repeat 2 --delay 80 1"
		case "right_click":
			cmd += " click 3"
		}
	case "type":
		if a.Text == "" || len(a.Text) > 4000 {
			return fmt.Errorf("type needs text of at most 4000 bytes")
		}
		cmd = "xdotool type --delay 12 -- " + shellQuote(a.Text)
	case "key":
		if !keyName.MatchString(a.Key) || len(a.Key) > 64 {
			return fmt.Errorf("key must be an xdotool key name such as Return, Tab or ctrl+l")
		}
		cmd = "xdotool key -- " + shellQuote(a.Key)
	case "scroll":
		button := map[string]int{"up": 4, "down": 5, "left": 6, "right": 7}[a.Scroll]
		if button == 0 {
			return fmt.Errorf("direction must be up, down, left or right")
		}
		if a.Amount < 1 || a.Amount > 30 {
			a.Amount = 3
		}
		if err := inside(); err != nil {
			return err
		}
		cmd = fmt.Sprintf("xdotool mousemove --sync %d %d click --repeat %d %d", a.X, a.Y, a.Amount, button)
	default:
		return fmt.Errorf("unknown action %q (click, double_click, right_click, move, type, key, scroll)", a.Type)
	}
	out, err := run(ctx, fmt.Sprintf(`# lectern-desktop-input
dir=%s; export DISPLAY=:%d
[ -d "$dir" ] || { echo "ERROR the desktop is gone"; exit 0; }
command -v xdotool >/dev/null 2>&1 || { echo "MISSING xdotool"; exit 0; }
%s >/dev/null 2>"$dir/xdotool.err" || { echo "ERROR $(tail -n 1 "$dir/xdotool.err")"; exit 0; }
echo OK
`, shellQuote(d.Dir), d.Display, cmd))
	if err != nil {
		return err
	}
	switch out = strings.TrimSpace(out); {
	case out == "OK":
		return nil
	case strings.HasPrefix(out, "MISSING"):
		return &MissingError{Tools: strings.Fields(out)[1:]}
	case strings.HasPrefix(out, "ERROR "):
		return fmt.Errorf("%s", strings.TrimPrefix(out, "ERROR "))
	}
	return fmt.Errorf("unexpected reply: %s", out)
}
