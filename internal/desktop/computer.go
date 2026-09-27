package desktop

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
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

// Window is one visible top-level window.
type Window struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Windows lists the desktop's visible windows, so an agent knows what is on
// screen and where before it clicks.
func Windows(ctx context.Context, run Runner, d *Desktop) ([]Window, error) {
	if !stateDir.MatchString(d.Dir) {
		return nil, fmt.Errorf("not a desktop state directory")
	}
	out, err := run(ctx, fmt.Sprintf(`# lectern-desktop-windows
dir=%s; export DISPLAY=:%d
[ -d "$dir" ] || { echo "ERROR the desktop is gone"; exit 0; }
command -v xdotool >/dev/null 2>&1 || { echo "MISSING xdotool"; exit 0; }
for id in $(xdotool search --onlyvisible --name '.' 2>/dev/null | head -n 50); do
  eval "$(xdotool getwindowgeometry --shell "$id" 2>/dev/null)"
  [ "${WIDTH:-0}" -gt 1 ] || continue
  printf 'WIN\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\n' "$id" "$X" "$Y" "$WIDTH" "$HEIGHT" "$(xdotool getwindowname "$id" 2>/dev/null | tr '\t\n' '  ')"
done
`, shellQuote(d.Dir), d.Display))
	if err != nil {
		return nil, err
	}
	wins := []Window{}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "MISSING"):
			return nil, &MissingError{Tools: strings.Fields(line)[1:]}
		case strings.HasPrefix(line, "ERROR "):
			return nil, fmt.Errorf("%s", strings.TrimPrefix(line, "ERROR "))
		case strings.HasPrefix(line, "WIN\t"):
			f := strings.SplitN(line, "\t", 7)
			if len(f) != 7 {
				continue
			}
			w := Window{ID: f[1], Name: f[6]}
			fmt.Sscan(f[2], &w.X)
			fmt.Sscan(f[3], &w.Y)
			fmt.Sscan(f[4], &w.Width)
			fmt.Sscan(f[5], &w.Height)
			wins = append(wins, w)
		}
	}
	return wins, nil
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

//go:embed a11y.py
var a11yScript string

// ErrNoA11y means the desktop has no accessibility bus to read.
var ErrNoA11y = fmt.Errorf("this desktop has no accessibility bus (install at-spi2-core, dbus and x11-utils " +
	"on its machine, then start a new desktop); coordinates still work")

func (d *Desktop) a11yRun(ctx context.Context, run Runner, args ...string) (string, error) {
	if !stateDir.MatchString(d.Dir) {
		return "", fmt.Errorf("not a desktop state directory")
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	out, err := run(ctx, fmt.Sprintf(`# lectern-desktop-a11y
dir=%s; export DISPLAY=:%d
[ -d "$dir" ] || { echo "ERROR the desktop is gone"; exit 0; }
[ -f "$dir/a11y" ] || { echo NOA11Y; exit 0; }
command -v python3 >/dev/null 2>&1 || { echo "MISSING python3"; exit 0; }
python3 -c %s %s 2>"$dir/a11y-script.err" || echo "ERROR $(tail -n 1 "$dir/a11y-script.err")"
`, shellQuote(d.Dir), d.Display, shellQuote(a11yScript), strings.Join(quoted, " ")))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case line == "NOA11Y":
			return "", ErrNoA11y
		case strings.HasPrefix(line, "MISSING"):
			return "", &MissingError{Tools: strings.Fields(line)[1:]}
		case strings.HasPrefix(line, "ERROR "):
			return "", fmt.Errorf("%s", strings.TrimPrefix(line, "ERROR "))
		case strings.HasPrefix(line, "TREE "), strings.HasPrefix(line, "OK "), strings.HasPrefix(line, "POINT "):
			return line, nil
		}
	}
	return "", fmt.Errorf("the accessibility reader said nothing: %s", strings.TrimSpace(out))
}

// A11ySnapshot reads the desktop's accessibility tree: every app, window and
// control, each actionable one with a [ref=N] and its screen rectangle.
func A11ySnapshot(ctx context.Context, run Runner, d *Desktop) (string, int, error) {
	line, err := d.a11yRun(ctx, run, "snapshot", d.Dir)
	if err != nil {
		return "", 0, err
	}
	var out struct {
		Tree string `json:"tree"`
		Refs int    `json:"refs"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "TREE ")), &out); err != nil {
		return "", 0, err
	}
	return out.Tree, out.Refs, nil
}

// A11yAct clicks, focuses or types into the element ref names. It uses the
// element's own accessible action where it has one, and otherwise clicks the
// middle of its rectangle; via says which happened.
func A11yAct(ctx context.Context, run Runner, d *Desktop, ref int, action, text string) (string, error) {
	if ref <= 0 {
		return "", fmt.Errorf("ref must come from a computer snapshot")
	}
	if action != "click" && action != "focus" && action != "type" {
		return "", fmt.Errorf("an element can be clicked, focused or typed into")
	}
	if action == "type" && (text == "" || len(text) > 4000) {
		return "", fmt.Errorf("type needs text of at most 4000 bytes")
	}
	line, err := d.a11yRun(ctx, run, "act", d.Dir, fmt.Sprint(ref), action, text)
	if err != nil {
		return "", err
	}
	if rest, ok := strings.CutPrefix(line, "OK "); ok {
		var r struct {
			Via string `json:"via"`
		}
		_ = json.Unmarshal([]byte(rest), &r)
		return r.Via, nil
	}
	var p struct {
		Point []int  `json:"point"`
		Then  string `json:"then"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "POINT ")), &p); err != nil || len(p.Point) != 2 {
		return "", fmt.Errorf("the element has no action and no place on screen to click")
	}
	x, y := p.Point[0], p.Point[1]
	if x < 0 || y < 0 || x >= d.Width || y >= d.Height {
		return "", fmt.Errorf("the element is off screen")
	}
	if action == "focus" || p.Then == "type" || action == "click" {
		if err := Act(ctx, run, d, Action{Type: "click", X: x, Y: y}); err != nil {
			return "", err
		}
	}
	if action == "type" {
		if err := Act(ctx, run, d, Action{Type: "type", Text: text}); err != nil {
			return "", err
		}
	}
	return "coordinates", nil
}
