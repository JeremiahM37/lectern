package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Target names an element: a ref from the last snapshot, or a CSS selector.
type Target struct {
	Ref      int    `json:"ref"`
	Selector string `json:"selector"`
}

func (b *Browser) resolve(ctx context.Context, t Target) (int64, error) {
	if t.Ref > 0 {
		b.mu.Lock()
		id, ok := b.refs[t.Ref]
		b.mu.Unlock()
		if !ok {
			return 0, fmt.Errorf("ref %d is not in the latest snapshot; take a new snapshot", t.Ref)
		}
		return id, nil
	}
	sel := strings.TrimSpace(t.Selector)
	if sel == "" {
		return 0, fmt.Errorf("name the element with ref (from a snapshot) or selector")
	}
	raw, _ := json.Marshal(sel)
	var res struct {
		Result struct {
			ObjectID string `json:"objectId"`
			Subtype  string `json:"subtype"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := b.conn.Call(ctx, b.page, "Runtime.evaluate", map[string]any{
		"expression": "document.querySelector(" + string(raw) + ")"}, &res); err != nil {
		return 0, err
	}
	if res.Exception != nil {
		return 0, fmt.Errorf("invalid selector %s", sel)
	}
	if res.Result.ObjectID == "" || res.Result.Subtype == "null" {
		return 0, fmt.Errorf("%w: %s", ErrNotFound, sel)
	}
	defer b.release(res.Result.ObjectID)
	var node struct {
		Node struct {
			BackendNodeID int64 `json:"backendNodeId"`
		} `json:"node"`
	}
	if err := b.conn.Call(ctx, b.page, "DOM.describeNode", map[string]any{"objectId": res.Result.ObjectID}, &node); err != nil {
		return 0, err
	}
	return node.Node.BackendNodeID, nil
}

func (b *Browser) release(objectID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5e9)
		defer cancel()
		_ = b.conn.Call(ctx, b.page, "Runtime.releaseObject", map[string]any{"objectId": objectID}, nil)
	}()
}

// center scrolls an element into view and returns its middle in viewport
// coordinates.
func (b *Browser) center(ctx context.Context, node int64) (float64, float64, error) {
	_ = b.conn.Call(ctx, b.page, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": node}, nil)
	var q struct {
		Quads [][]float64 `json:"quads"`
	}
	if err := b.conn.Call(ctx, b.page, "DOM.getContentQuads", map[string]any{"backendNodeId": node}, &q); err != nil {
		return 0, 0, fmt.Errorf("the element is not visible: %w", err)
	}
	for _, quad := range q.Quads {
		if len(quad) != 8 {
			continue
		}
		x := (quad[0] + quad[2] + quad[4] + quad[6]) / 4
		y := (quad[1] + quad[3] + quad[5] + quad[7]) / 4
		return x, y, nil
	}
	return 0, 0, fmt.Errorf("the element is not visible")
}

// Click clicks an element's middle, the way a person would.
func (b *Browser) Click(ctx context.Context, t Target) (State, error) {
	node, err := b.resolve(ctx, t)
	if err != nil {
		return State{}, err
	}
	x, y, err := b.center(ctx, node)
	if err != nil {
		return State{}, err
	}
	if err := b.ClickAt(ctx, x, y); err != nil {
		return State{}, err
	}
	return b.State(), nil
}

// ClickAt clicks a point in the viewport.
func (b *Browser) ClickAt(ctx context.Context, x, y float64) error {
	for _, step := range []map[string]any{
		{"type": "mouseMoved", "x": x, "y": y},
		{"type": "mousePressed", "x": x, "y": y, "button": "left", "clickCount": 1},
		{"type": "mouseReleased", "x": x, "y": y, "button": "left", "clickCount": 1},
	} {
		if err := b.conn.Call(ctx, b.page, "Input.dispatchMouseEvent", step, nil); err != nil {
			// The click closed its own page (window.close()): it happened.
			if errors.Is(err, ErrPageGone) {
				return nil
			}
			return err
		}
	}
	return nil
}

// Fill replaces an input's value with text, as typing would, so the page's own
// input handlers run. A select element picks the option by value or label.
func (b *Browser) Fill(ctx context.Context, t Target, text string) (State, error) {
	node, err := b.resolve(ctx, t)
	if err != nil {
		return State{}, err
	}
	var obj struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if err := b.conn.Call(ctx, b.page, "DOM.resolveNode", map[string]any{"backendNodeId": node}, &obj); err != nil {
		return State{}, err
	}
	defer b.release(obj.Object.ObjectID)
	var kind struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	// Clear, focus, and report what kind of control this is.
	if err := b.conn.Call(ctx, b.page, "Runtime.callFunctionOn", map[string]any{"objectId": obj.Object.ObjectID,
		"returnByValue": true, "functionDeclaration": `function(v){
  this.scrollIntoView({block:'center'});
  if (this.tagName === 'SELECT') {
    const o = Array.from(this.options).find(o => o.value === v || o.label.trim() === v.trim());
    if (!o) return 'nooption';
    this.value = o.value;
    this.dispatchEvent(new Event('input', {bubbles: true}));
    this.dispatchEvent(new Event('change', {bubbles: true}));
    return 'select';
  }
  this.focus();
  if ('value' in this && typeof this.select === 'function') { this.select(); return 'input'; }
  if (this.isContentEditable) { document.execCommand('selectAll'); return 'editable'; }
  return 'other';
}`, "arguments": []map[string]any{{"value": text}}}, &kind); err != nil {
		return State{}, err
	}
	switch kind.Result.Value {
	case "select":
		return b.State(), nil
	case "nooption":
		return State{}, fmt.Errorf("the select has no option %q", text)
	case "other":
		return State{}, fmt.Errorf("the element is not a text field, select or editable region")
	}
	if text == "" {
		if err := b.Press(ctx, "Delete"); err != nil {
			return State{}, err
		}
	} else if err := b.conn.Call(ctx, b.page, "Input.insertText", map[string]any{"text": text}, nil); err != nil {
		return State{}, err
	}
	_ = b.conn.Call(ctx, b.page, "Runtime.callFunctionOn", map[string]any{"objectId": obj.Object.ObjectID,
		"functionDeclaration": `function(){ this.dispatchEvent(new Event('change', {bubbles: true})); }`}, nil)
	return b.State(), nil
}

type keyDef struct {
	key, code string
	keyCode   int
	text      string
}

var namedKeys = map[string]keyDef{
	"enter": {"Enter", "Enter", 13, "\r"}, "tab": {"Tab", "Tab", 9, ""}, "escape": {"Escape", "Escape", 27, ""},
	"esc": {"Escape", "Escape", 27, ""}, "backspace": {"Backspace", "Backspace", 8, ""},
	"delete": {"Delete", "Delete", 46, ""}, "space": {" ", "Space", 32, " "},
	"arrowup": {"ArrowUp", "ArrowUp", 38, ""}, "arrowdown": {"ArrowDown", "ArrowDown", 40, ""},
	"arrowleft": {"ArrowLeft", "ArrowLeft", 37, ""}, "arrowright": {"ArrowRight", "ArrowRight", 39, ""},
	"home": {"Home", "Home", 36, ""}, "end": {"End", "End", 35, ""},
	"pageup": {"PageUp", "PageUp", 33, ""}, "pagedown": {"PageDown", "PageDown", 34, ""},
}

var modifierBits = map[string]int{"alt": 1, "control": 2, "ctrl": 2, "meta": 4, "cmd": 4, "shift": 8}

// parseKey reads "Enter", "a", "Control+a" or "Shift+Tab".
func parseKey(combo string) (keyDef, int, error) {
	combo = strings.TrimSpace(combo)
	last, prefix := combo, ""
	if strings.HasSuffix(combo, "+") && len(combo) > 1 {
		last, prefix = "+", strings.TrimSuffix(strings.TrimSuffix(combo, "+"), "+")
	} else if i := strings.LastIndex(combo, "+"); i > 0 {
		last, prefix = combo[i+1:], combo[:i]
	}
	mods := 0
	if prefix != "" {
		for _, p := range strings.Split(prefix, "+") {
			bit, ok := modifierBits[strings.ToLower(strings.TrimSpace(p))]
			if !ok {
				return keyDef{}, 0, fmt.Errorf("unknown modifier %q", p)
			}
			mods |= bit
		}
	}
	if def, ok := namedKeys[strings.ToLower(last)]; ok {
		return def, mods, nil
	}
	if r := []rune(last); len(r) == 1 {
		ch := string(r[0])
		def := keyDef{key: ch, text: ch}
		upper := strings.ToUpper(ch)
		if upper >= "A" && upper <= "Z" && len(upper) == 1 {
			def.code, def.keyCode = "Key"+upper, int(upper[0])
		} else if ch >= "0" && ch <= "9" {
			def.code, def.keyCode = "Digit"+ch, int(ch[0])
		}
		return def, mods, nil
	}
	return keyDef{}, 0, fmt.Errorf("unknown key %q (use a single character or Enter, Tab, Escape, Backspace, Delete, Space, Arrow keys, Home, End, PageUp, PageDown)", last)
}

// Press sends one key, optionally with modifiers.
func (b *Browser) Press(ctx context.Context, combo string) error {
	def, mods, err := parseKey(combo)
	if err != nil {
		return err
	}
	down := map[string]any{"type": "keyDown", "key": def.key, "code": def.code,
		"windowsVirtualKeyCode": def.keyCode, "modifiers": mods}
	// Only a plain or shifted key produces a character; Control+a selects.
	if def.text != "" && mods&^8 == 0 {
		down["text"] = def.text
	} else {
		down["type"] = "rawKeyDown"
	}
	if err := b.conn.Call(ctx, b.page, "Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	return b.conn.Call(ctx, b.page, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": def.key,
		"code": def.code, "windowsVirtualKeyCode": def.keyCode, "modifiers": mods}, nil)
}

// ---- the operator's own input from the pane -----------------------------------

// Input is one mouse, key or text event from the Browser pane, in viewport
// CSS pixels.
type Input struct {
	Type      string  `json:"type"` // mousemove|mousedown|mouseup|wheel|keydown|keyup|text
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Button    string  `json:"button"`
	Clicks    int     `json:"clicks"`
	DeltaX    float64 `json:"dx"`
	DeltaY    float64 `json:"dy"`
	Key       string  `json:"key"`
	Code      string  `json:"code"`
	KeyCode   int     `json:"key_code"`
	Text      string  `json:"text"`
	Modifiers int     `json:"modifiers"`
}

// Dispatch replays one pane event into the page.
func (b *Browser) Dispatch(ctx context.Context, in Input) error {
	button := in.Button
	if button != "left" && button != "right" && button != "middle" {
		button = "left"
	}
	if in.Clicks < 1 || in.Clicks > 3 {
		in.Clicks = 1
	}
	mods := in.Modifiers & 15
	switch in.Type {
	case "mousemove":
		return b.conn.Call(ctx, b.page, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": in.X, "y": in.Y, "modifiers": mods}, nil)
	case "mousedown", "mouseup":
		kind := map[string]string{"mousedown": "mousePressed", "mouseup": "mouseReleased"}[in.Type]
		return b.conn.Call(ctx, b.page, "Input.dispatchMouseEvent", map[string]any{"type": kind, "x": in.X, "y": in.Y,
			"button": button, "clickCount": in.Clicks, "modifiers": mods}, nil)
	case "wheel":
		return b.conn.Call(ctx, b.page, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": in.X, "y": in.Y,
			"deltaX": in.DeltaX, "deltaY": in.DeltaY, "modifiers": mods}, nil)
	case "keydown", "keyup":
		if len(in.Key) > 32 || len(in.Code) > 32 {
			return fmt.Errorf("invalid key")
		}
		ev := map[string]any{"type": "keyUp", "key": in.Key, "code": in.Code, "windowsVirtualKeyCode": in.KeyCode, "modifiers": mods}
		if in.Type == "keydown" {
			ev["type"] = "rawKeyDown"
			if r := []rune(in.Key); len(r) == 1 && mods&^8 == 0 {
				ev["type"], ev["text"] = "keyDown", in.Key
			} else if in.Key == "Enter" && mods == 0 {
				ev["type"], ev["text"] = "keyDown", "\r"
			}
		}
		return b.conn.Call(ctx, b.page, "Input.dispatchKeyEvent", ev, nil)
	case "text":
		if len(in.Text) > 4000 {
			return fmt.Errorf("text is too long")
		}
		return b.conn.Call(ctx, b.page, "Input.insertText", map[string]any{"text": in.Text}, nil)
	}
	return fmt.Errorf("unknown input type %q", in.Type)
}
