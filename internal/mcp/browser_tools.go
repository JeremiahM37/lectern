package mcp

import (
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/mediapost"
)

// Browser and computer-use tools (docs/browser.md). The browser is a real
// headless Chromium on the machine this session runs on, shared with the
// operator's Browser pane: they watch every step and can take over. Results
// that carry a "screenshot" come back as an MCP image block.

func sessionHints(args map[string]any) map[string]any {
	body := map[string]any{}
	if id := argInt(args, "session_id"); id > 0 {
		body["session_id"] = id
	} else {
		body["hint_session_id"] = mediapost.SessionID()
		body["tmux_session"] = mediapost.TmuxSession()
	}
	return body
}

// browserCall posts one action to the session's browser.
func (s *Server) browserCall(action string, args map[string]any, keys ...string) (any, error) {
	body := sessionHints(args)
	body["action"] = action
	for _, k := range keys {
		if v, ok := args[k]; ok {
			body[k] = v
		}
	}
	return s.apiLong("POST", "/browser", body, 100*time.Second)
}

func (s *Server) computerCall(action string, args map[string]any, keys ...string) (any, error) {
	body := sessionHints(args)
	body["action"] = action
	for _, k := range append(keys, "live_id") {
		if v, ok := args[k]; ok {
			body[k] = v
		}
	}
	return s.apiLong("POST", "/computer", body, 70*time.Second)
}

var sessionArg = num("Lectern session whose browser to use; omit to use the session you are running in")

func init() {
	ref := num("element ref from the latest browser_snapshot, e.g. 12")
	selector := str("CSS selector, when there is no ref, e.g. '#submit'")
	tools = append(tools,
		tool{
			Name: "browser_navigate",
			Description: "Open an address in this session's browser: a real Chromium on the machine you run on, so " +
				"http://localhost:PORT is your own dev server. The operator watches it live in Lectern's Browser pane. " +
				"Starts the browser if needed. Then use browser_snapshot to read the page.",
			Schema: obj(map[string]any{"url": str("http(s) address, e.g. http://localhost:5173/settings"), "session_id": sessionArg}, "url"),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.browserCall("navigate", args, "url")
			},
		},
		tool{
			Name: "browser_snapshot",
			Description: "Read the current page as an accessibility outline in which every element you can act on has " +
				"a [ref=N], plus a screenshot. Pass refs to browser_click and browser_fill. Refs from an older snapshot " +
				"stop working once the page changes; take a new one.",
			Schema: obj(map[string]any{"screenshot": flag("include a screenshot (default true)"), "session_id": sessionArg}),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.browserCall("snapshot", args, "screenshot")
			},
		},
		tool{
			Name:        "browser_click",
			Description: "Click an element by ref (from browser_snapshot) or CSS selector, or a point by x and y in CSS pixels.",
			Schema: obj(map[string]any{"ref": ref, "selector": selector, "x": num("x in CSS pixels, with y, when clicking a point"),
				"y": num("y in CSS pixels"), "session_id": sessionArg}),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.browserCall("click", args, "ref", "selector", "x", "y")
			},
		},
		tool{
			Name:        "browser_fill",
			Description: "Replace a text field's value (or pick a select option by value or label), as typing would.",
			Schema:      obj(map[string]any{"ref": ref, "selector": selector, "text": str("the new value"), "session_id": sessionArg}, "text"),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.browserCall("fill", args, "ref", "selector", "text")
			},
		},
		tool{
			Name:        "browser_press",
			Description: "Press a key in the page: Enter, Tab, Escape, Backspace, ArrowDown, a single character, or a combination such as Control+a.",
			Schema:      obj(map[string]any{"key": str("key or combination, e.g. Enter or Shift+Tab"), "session_id": sessionArg}, "key"),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.browserCall("press", args, "key")
			},
		},
		tool{
			Name: "browser_evaluate",
			Description: "Evaluate a read-only JavaScript expression in the page and return its value, e.g. " +
				"document.querySelector('h1').textContent. Anything that would change the page is refused; use click, fill and press to act.",
			Schema: obj(map[string]any{"expression": str("a JavaScript expression"), "session_id": sessionArg}, "expression"),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.browserCall("evaluate", args, "expression")
			},
		},
		tool{
			Name:        "browser_console",
			Description: "Recent console messages and uncaught exceptions from the page, oldest first.",
			Schema:      obj(map[string]any{"limit": num("how many (default 100)"), "session_id": sessionArg}),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.browserCall("console", args, "limit")
			},
		},
		tool{
			Name:        "browser_network",
			Description: "Recent requests the page made, with status codes and failures, oldest first.",
			Schema:      obj(map[string]any{"limit": num("how many (default 100)"), "session_id": sessionArg}),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.browserCall("network", args, "limit")
			},
		},
		tool{
			Name:        "browser_screenshot",
			Description: "A screenshot of the viewport, or of one element when selector is given.",
			Schema:      obj(map[string]any{"selector": str("CSS selector of one element to crop to"), "session_id": sessionArg}),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.browserCall("screenshot", args, "selector")
			},
		},
		tool{
			Name:        "browser_history",
			Description: "Go back, forward, or reload the page.",
			Schema:      obj(map[string]any{"direction": str("back | forward | reload"), "session_id": sessionArg}, "direction"),
			Run: func(s *Server, args map[string]any) (any, error) {
				dir := argStr(args, "direction")
				if dir != "back" && dir != "forward" && dir != "reload" {
					dir = "reload"
				}
				return s.browserCall(dir, args)
			},
		},
		tool{
			Name:        "browser_resize",
			Description: "Change the viewport, e.g. 390x844 with mobile=true for a phone, to check a responsive layout.",
			Schema: obj(map[string]any{"width": num("CSS pixels"), "height": num("CSS pixels"),
				"mobile": flag("emulate a touch phone"), "session_id": sessionArg}, "width", "height"),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.browserCall("resize", args, "width", "height", "mobile")
			},
		},
		tool{
			Name:        "browser_close",
			Description: "Close this session's browser when you are done with it.",
			Schema:      obj(map[string]any{"session_id": sessionArg}),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.browserCall("close", args)
			},
		},
		tool{
			Name: "computer_screenshot",
			Description: "Screenshot this session's live desktop (start one with open_live_view). Computer use is off " +
				"unless the operator allowed it for this project or desktop; the error says so.",
			Schema: obj(map[string]any{"live_id": num("which desktop, when the session has several"), "session_id": sessionArg}),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.computerCall("screenshot", args)
			},
		},
		tool{
			Name:        "computer_click",
			Description: "Click a point on this session's live desktop, in screen pixels from computer_screenshot.",
			Schema: obj(map[string]any{"x": num("x in screen pixels"), "y": num("y in screen pixels"),
				"button": str("left (default), right or double"), "screenshot": flag("return a screenshot afterwards"),
				"live_id": num("which desktop"), "session_id": sessionArg}, "x", "y"),
			Run: func(s *Server, args map[string]any) (any, error) {
				action := map[string]string{"right": "right_click", "double": "double_click"}[argStr(args, "button")]
				if action == "" {
					action = "click"
				}
				return s.computerCall(action, args, "x", "y", "screenshot")
			},
		},
		tool{
			Name:        "computer_type",
			Description: "Type text into whatever has focus on this session's live desktop.",
			Schema: obj(map[string]any{"text": str("text to type"), "screenshot": flag("return a screenshot afterwards"),
				"live_id": num("which desktop"), "session_id": sessionArg}, "text"),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.computerCall("type", args, "text", "screenshot")
			},
		},
		tool{
			Name:        "computer_key",
			Description: "Press a key on this session's live desktop, as an xdotool key name: Return, Tab, Escape, ctrl+l, alt+F4.",
			Schema: obj(map[string]any{"key": str("xdotool key name"), "screenshot": flag("return a screenshot afterwards"),
				"live_id": num("which desktop"), "session_id": sessionArg}, "key"),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.computerCall("key", args, "key", "screenshot")
			},
		},
		tool{
			Name:        "computer_scroll",
			Description: "Scroll at a point on this session's live desktop.",
			Schema: obj(map[string]any{"x": num("x"), "y": num("y"), "direction": str("up | down | left | right"),
				"amount": num("wheel clicks (default 3)"), "live_id": num("which desktop"), "session_id": sessionArg}, "x", "y", "direction"),
			Run: func(s *Server, args map[string]any) (any, error) {
				return s.computerCall("scroll", args, "x", "y", "direction", "amount")
			},
		},
	)
	// A browser or a desktop on the operator's machine is driven by that
	// session's own agent, never by a remote chat through the web connector.
	for _, t := range tools {
		if strings.HasPrefix(t.Name, "browser_") || strings.HasPrefix(t.Name, "computer_") {
			webExcluded[t.Name] = true
		}
	}
}
