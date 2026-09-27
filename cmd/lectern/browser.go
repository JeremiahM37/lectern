package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/console"
	"github.com/JeremiahM37/lectern/v2/internal/mediapost"
)

// `lectern browser` and `lectern computer` are the agent browser and computer
// use tools for agents with no MCP support (docs/browser.md). They post to the
// same endpoints the MCP tools do and find their session the same way.

const browserUsage = `usage: lectern browser ACTION [ARGS] [--session ID]
  open|navigate URL           load an address (starts the session's browser)
  snapshot [--screenshot FILE] read the page as an outline with [ref=N]s
  click REF | --selector S | --at X,Y
  fill REF|--selector S TEXT  replace a field's value
  press KEY                   Enter, Tab, Escape, Control+a, ...
  eval EXPRESSION             read-only JavaScript
  console | network [--limit N]
  screenshot FILE [--selector S]
  back | forward | reload
  resize WIDTHxHEIGHT [--mobile]
  status | close`

const computerUsage = `usage: lectern computer ACTION [ARGS] [--session ID] [--live ID]
  snapshot                    the accessibility tree, with [ref=N]s
  screenshot FILE
  click REF | click X Y [--right|--double]
  type [--ref N] TEXT
  key KEY                     xdotool key name: Return, ctrl+l, ...
  scroll X Y up|down|left|right [--amount N]
  windows | status`

// commonFlags pulls --session, --live and flag values out of args.
func commonFlags(args []string, valued map[string]bool) (rest []string, flags map[string]string, err error) {
	flags = map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") || len(a) < 3 {
			rest = append(rest, a)
			continue
		}
		name := a[2:]
		if valued[name] {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("%s needs a value", a)
			}
			i++
			flags[name] = args[i]
			continue
		}
		flags[name] = "true"
	}
	return rest, flags, nil
}

func withSession(body map[string]any, flags map[string]string) error {
	if v := flags["session"]; v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("--session takes a session id")
		}
		body["session_id"] = id
		return nil
	}
	body["hint_session_id"] = mediapost.SessionID()
	body["tmux_session"] = mediapost.TmuxSession()
	return nil
}

// saveShot writes a result's screenshot to file and drops it from the output.
func saveShot(out map[string]any, file string) error {
	shot, _ := out["screenshot"].(string)
	delete(out, "screenshot")
	if file == "" {
		return nil
	}
	if shot == "" {
		return fmt.Errorf("the result has no screenshot")
	}
	png, err := base64.StdEncoding.DecodeString(shot)
	if err != nil {
		return err
	}
	if err := os.WriteFile(file, png, 0o600); err != nil {
		return err
	}
	out["screenshot_file"] = file
	return nil
}

func browserCommand(c *console.Client, args []string) ([]byte, error) {
	args, flags, err := commonFlags(args, map[string]bool{"session": true, "selector": true, "at": true,
		"screenshot": true, "limit": true})
	if err != nil {
		return nil, err
	}
	if len(args) == 0 || args[0] == "help" {
		return nil, fmt.Errorf("%s", browserUsage)
	}
	action, rest := args[0], args[1:]
	body := map[string]any{"action": action}
	if err := withSession(body, flags); err != nil {
		return nil, err
	}
	need := func(n int, what string) error {
		if len(rest) != n {
			return fmt.Errorf("usage: lectern browser %s %s", action, what)
		}
		return nil
	}
	target := func() error {
		if s := flags["selector"]; s != "" {
			body["selector"] = s
			return nil
		}
		if at := flags["at"]; at != "" {
			x, y, ok := strings.Cut(at, ",")
			fx, e1 := strconv.ParseFloat(x, 64)
			fy, e2 := strconv.ParseFloat(y, 64)
			if !ok || e1 != nil || e2 != nil {
				return fmt.Errorf("--at takes X,Y")
			}
			body["x"], body["y"] = fx, fy
			return nil
		}
		if len(rest) == 0 {
			return fmt.Errorf("name the element: a ref from snapshot, --selector S, or --at X,Y")
		}
		ref, err := strconv.Atoi(strings.TrimPrefix(rest[0], "ref="))
		if err != nil || ref <= 0 {
			return fmt.Errorf("%q is not a ref; use a number from snapshot, or --selector", rest[0])
		}
		body["ref"] = ref
		rest = rest[1:]
		return nil
	}
	shotFile := flags["screenshot"]
	switch action {
	case "open", "navigate":
		if err := need(1, "URL"); err != nil {
			return nil, err
		}
		body["action"], body["url"] = "navigate", rest[0]
	case "snapshot":
		body["screenshot"] = shotFile != ""
	case "click":
		if err := target(); err != nil {
			return nil, err
		}
	case "fill":
		if err := target(); err != nil {
			return nil, err
		}
		if len(rest) != 1 {
			return nil, fmt.Errorf("usage: lectern browser fill REF|--selector S TEXT")
		}
		body["text"] = rest[0]
	case "press":
		if err := need(1, "KEY"); err != nil {
			return nil, err
		}
		body["key"] = rest[0]
	case "eval", "evaluate":
		if err := need(1, "EXPRESSION"); err != nil {
			return nil, err
		}
		body["action"], body["expression"] = "evaluate", rest[0]
	case "console", "network":
		if v := flags["limit"]; v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("--limit takes a number")
			}
			body["limit"] = n
		}
	case "screenshot":
		if err := need(1, "FILE [--selector S]"); err != nil {
			return nil, err
		}
		shotFile = rest[0]
		if s := flags["selector"]; s != "" {
			body["selector"] = s
		}
	case "resize":
		if err := need(1, "WIDTHxHEIGHT [--mobile]"); err != nil {
			return nil, err
		}
		w, h, ok := strings.Cut(strings.ToLower(rest[0]), "x")
		wi, e1 := strconv.Atoi(w)
		hi, e2 := strconv.Atoi(h)
		if !ok || e1 != nil || e2 != nil {
			return nil, fmt.Errorf("resize takes WIDTHxHEIGHT, e.g. 390x844")
		}
		body["width"], body["height"], body["mobile"] = wi, hi, flags["mobile"] == "true"
	case "back", "forward", "reload", "status", "close":
	default:
		return nil, fmt.Errorf("unknown browser action %q\n%s", action, browserUsage)
	}
	raw, err := c.JSON("POST", "/browser", body)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return raw, nil
	}
	if err := saveShot(out, shotFile); err != nil {
		return nil, err
	}
	// A snapshot is for reading: print the outline itself, not JSON.
	if action == "snapshot" {
		tree, _ := out["tree"].(string)
		text := fmt.Sprintf("%v — %v\n\n%s", out["title"], out["url"], tree)
		if f, _ := out["screenshot_file"].(string); f != "" {
			text += "\nscreenshot: " + f + "\n"
		}
		return []byte(text), nil
	}
	return json.MarshalIndent(out, "", "  ")
}

func computerCommand(c *console.Client, args []string) ([]byte, error) {
	args, flags, err := commonFlags(args, map[string]bool{"session": true, "live": true, "amount": true, "ref": true})
	if err != nil {
		return nil, err
	}
	if len(args) == 0 || args[0] == "help" {
		return nil, fmt.Errorf("%s", computerUsage)
	}
	action, rest := args[0], args[1:]
	body := map[string]any{"action": action}
	if err := withSession(body, flags); err != nil {
		return nil, err
	}
	if v := flags["live"]; v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("--live takes a desktop id")
		}
		body["live_id"] = id
	}
	point := func() error {
		if len(rest) < 2 {
			return fmt.Errorf("%s", computerUsage)
		}
		x, e1 := strconv.Atoi(rest[0])
		y, e2 := strconv.Atoi(rest[1])
		if e1 != nil || e2 != nil {
			return fmt.Errorf("X and Y are whole screen pixels")
		}
		body["x"], body["y"] = x, y
		rest = rest[2:]
		return nil
	}
	shotFile := ""
	switch action {
	case "screenshot":
		if len(rest) != 1 {
			return nil, fmt.Errorf("usage: lectern computer screenshot FILE")
		}
		shotFile = rest[0]
	case "click":
		if len(rest) == 1 {
			ref, err := strconv.Atoi(strings.TrimPrefix(rest[0], "ref="))
			if err != nil || ref <= 0 {
				return nil, fmt.Errorf("click takes a ref from snapshot, or X Y")
			}
			body["ref"] = ref
			break
		}
		if err := point(); err != nil {
			return nil, err
		}
		if flags["right"] == "true" {
			body["action"] = "right_click"
		} else if flags["double"] == "true" {
			body["action"] = "double_click"
		}
	case "type":
		if len(rest) != 1 {
			return nil, fmt.Errorf("usage: lectern computer type TEXT")
		}
		body["text"] = rest[0]
		if v := flags["ref"]; v != "" {
			ref, err := strconv.Atoi(v)
			if err != nil || ref <= 0 {
				return nil, fmt.Errorf("--ref takes a ref from snapshot")
			}
			body["ref"] = ref
		}
	case "key":
		if len(rest) != 1 {
			return nil, fmt.Errorf("usage: lectern computer key KEY")
		}
		body["key"] = rest[0]
	case "scroll":
		if err := point(); err != nil {
			return nil, err
		}
		if len(rest) != 1 {
			return nil, fmt.Errorf("usage: lectern computer scroll X Y up|down|left|right")
		}
		body["direction"] = rest[0]
		if v := flags["amount"]; v != "" {
			n, _ := strconv.Atoi(v)
			body["amount"] = n
		}
	case "status", "windows", "snapshot":
	default:
		return nil, fmt.Errorf("unknown computer action %q\n%s", action, computerUsage)
	}
	raw, err := c.JSON("POST", "/computer", body)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return raw, nil
	}
	if err := saveShot(out, shotFile); err != nil {
		return nil, err
	}
	return json.MarshalIndent(out, "", "  ")
}
