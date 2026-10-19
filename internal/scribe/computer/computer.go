// Package computer runs the actions of Anthropic's computer-use tool on this
// machine. It drives a virtual display through the desktop driver that Lectern
// already uses for remote browsers, so the same xdotool and screenshot code
// runs here.
package computer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/desktop"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// ToolName is the name Anthropic's computer-use tool is called by.
const ToolName = "computer"

// Display size. It matches the size the Anthropic adapter declares.
const (
	Width  = 1024
	Height = 768
)

// maxWait caps the wait action, so one call cannot stall a turn for long.
const maxWait = 30 * time.Second

// Runner executes computer-use actions. It starts its display the first time
// an action needs one, and keeps it until Close.
type Runner struct {
	mu      sync.Mutex
	run     desktop.Runner
	owner   string
	d       *desktop.Desktop
	allowed bool // the user chose "allow for this session" for input actions
}

// New returns a Runner that runs its commands on this machine.
func New() *Runner {
	return &Runner{run: localRun, owner: fmt.Sprintf("scribe%d", os.Getpid())}
}

// localRun runs one desktop script with bash, as the driver expects.
func localRun(ctx context.Context, script string) (string, error) {
	out, err := exec.CommandContext(ctx, "bash", "-c", script).Output()
	return string(out), err
}

// input is the computer-use tool's input, as Anthropic defines it.
type input struct {
	Action          string  `json:"action"`
	Coordinate      []int   `json:"coordinate"`
	Text            string  `json:"text"`
	Duration        float64 `json:"duration"`
	ScrollDirection string  `json:"scroll_direction"`
	ScrollAmount    int     `json:"scroll_amount"`
}

// Run performs one computer-use call. host is asked before any action that
// changes the screen or types; screenshots and waits never ask.
func (r *Runner) Run(ctx context.Context, host tools.Host, args json.RawMessage) tools.Result {
	var in input
	if err := json.Unmarshal(args, &in); err != nil {
		return tools.Errorf("computer: the input is not valid JSON: %v", err)
	}
	switch in.Action {
	case "screenshot":
		d, err := r.display(ctx)
		if err != nil {
			return tools.Errorf("computer: %v", err)
		}
		png, err := desktop.Screenshot(ctx, r.run, d)
		if err != nil {
			return tools.Errorf("computer: screenshot failed: %v", err)
		}
		return tools.Result{Output: "screenshot", Image: png, ImageType: "image/png"}
	case "wait":
		wait := time.Duration(in.Duration * float64(time.Second))
		if wait <= 0 {
			wait = time.Second
		}
		if wait > maxWait {
			wait = maxWait
		}
		select {
		case <-time.After(wait):
			return tools.Result{Output: "waited"}
		case <-ctx.Done():
			return tools.Errorf("computer: wait cancelled")
		}
	}
	act, summary, err := toAction(in)
	if err != nil {
		return tools.Errorf("computer: %v", err)
	}
	if denied := r.ask(ctx, host, summary); denied != "" {
		return tools.Errorf("%s", denied)
	}
	d, err := r.display(ctx)
	if err != nil {
		return tools.Errorf("computer: %v", err)
	}
	if err := desktop.Act(ctx, r.run, d, act); err != nil {
		return tools.Errorf("computer: %s failed: %v", in.Action, err)
	}
	return tools.Result{Output: "done"}
}

// toAction maps an Anthropic action onto the driver's. summary is the line the
// user sees in the approval prompt.
func toAction(in input) (desktop.Action, string, error) {
	point := func() (int, int, error) {
		if len(in.Coordinate) != 2 {
			return 0, 0, fmt.Errorf("%s needs a coordinate [x, y]", in.Action)
		}
		return in.Coordinate[0], in.Coordinate[1], nil
	}
	switch in.Action {
	case "left_click", "right_click", "double_click", "mouse_move":
		x, y, err := point()
		if err != nil {
			return desktop.Action{}, "", err
		}
		kind := map[string]string{"left_click": "click", "right_click": "right_click", "double_click": "double_click", "mouse_move": "move"}[in.Action]
		return desktop.Action{Type: kind, X: x, Y: y}, fmt.Sprintf("%s at (%d, %d)", in.Action, x, y), nil
	case "type":
		if in.Text == "" {
			return desktop.Action{}, "", fmt.Errorf("type needs text")
		}
		return desktop.Action{Type: "type", Text: in.Text}, fmt.Sprintf("type %d characters", len(in.Text)), nil
	case "key":
		if in.Text == "" {
			return desktop.Action{}, "", fmt.Errorf("key needs a key name in text")
		}
		return desktop.Action{Type: "key", Key: in.Text}, "press " + in.Text, nil
	case "scroll":
		x, y := Width/2, Height/2
		if len(in.Coordinate) == 2 {
			x, y = in.Coordinate[0], in.Coordinate[1]
		}
		amount := in.ScrollAmount
		if amount < 1 {
			amount = 3
		}
		return desktop.Action{Type: "scroll", X: x, Y: y, Scroll: in.ScrollDirection, Amount: amount},
			fmt.Sprintf("scroll %s by %d", in.ScrollDirection, amount), nil
	case "":
		return desktop.Action{}, "", fmt.Errorf("the input has no action")
	}
	return desktop.Action{}, "", fmt.Errorf("the action %q is not supported here (screenshot, wait, left_click, right_click, double_click, mouse_move, type, key and scroll are)", in.Action)
}

// ask returns "" when the action may run, or the reason it may not.
func (r *Runner) ask(ctx context.Context, host tools.Host, summary string) string {
	r.mu.Lock()
	allowed := r.allowed
	r.mu.Unlock()
	if allowed {
		return ""
	}
	if host == nil {
		return "computer: this action needs the user's approval, but no way to ask is available."
	}
	switch host.Approve(ctx, tools.Approval{
		Tool:   ToolName,
		Kind:   "computer",
		Title:  "Let the agent use this computer",
		Detail: summary,
	}) {
	case tools.AllowSession:
		r.mu.Lock()
		r.allowed = true
		r.mu.Unlock()
		return ""
	case tools.AllowOnce:
		return ""
	}
	return "computer: the user declined " + summary + "."
}

// display returns the running desktop, starting it if needed.
func (r *Runner) display(ctx context.Context) (*desktop.Desktop, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.d != nil {
		return r.d, nil
	}
	d, err := desktop.Start(ctx, r.run, r.owner, Width, Height)
	if err != nil {
		return nil, err
	}
	r.d = d
	return d, nil
}

// Close stops the display if one is running. The app calls it when the
// session closes.
func (r *Runner) Close(ctx context.Context) error {
	r.mu.Lock()
	d := r.d
	r.d = nil
	r.mu.Unlock()
	if d == nil {
		return nil
	}
	return desktop.Stop(ctx, r.run, d.Dir)
}
