package mods

import (
	"context"
	"fmt"
	"strings"
)

// Element is a mod element ($.ui.resolve) as the console draws it. Props a
// terminal cannot show are dropped.
type Element struct {
	Type      string // Box, Text, Badge, Button, Link
	Text      string // Text, Badge
	Tone      string // dim, accent, warn, danger, ok
	Bold      bool
	Mono      bool
	Label     string // Button, Link
	Href      string // Link
	Hotkey    string // Button
	Direction string // Box: row or column
	Gap       int
	Children  []Element

	press func()
}

// Pressable reports whether the element is a button with an onPress.
func (e Element) Pressable() bool { return e.press != nil }

// Press runs the button's onPress in its mod, without waiting for it.
func (e Element) Press() {
	if e.press != nil {
		e.press()
	}
}

// Buttons lists the buttons in elems, depth first.
func Buttons(elems []Element) []Element {
	var out []Element
	for _, e := range elems {
		if e.Type == "Button" {
			out = append(out, e)
		}
		out = append(out, Buttons(e.Children)...)
	}
	return out
}

var elementTypes = map[string]bool{"Box": true, "Text": true, "Badge": true, "Button": true, "Link": true}

const maxElementDepth = 16

func (h *Host) element(v any, depth int) (Element, bool) {
	if depth > maxElementDepth {
		return Element{}, false
	}
	if s, ok := v.(string); ok {
		return Element{Type: "Text", Text: s}, true
	}
	m, ok := v.(map[string]any)
	if !ok {
		return Element{}, false
	}
	typ, _ := m["type"].(string)
	if !elementTypes[typ] {
		return Element{}, false
	}
	props, _ := m["props"].(map[string]any)
	text := func(k string) string {
		switch x := props[k].(type) {
		case string:
			return x
		case nil:
			return ""
		case map[string]any, []any:
			return ""
		default:
			return fmt.Sprint(x)
		}
	}
	e := Element{Type: typ, Text: text("text"), Tone: text("tone"), Bold: truthy(props["bold"]), Mono: truthy(props["mono"]),
		Label: text("label"), Href: text("href"), Hotkey: text("hotkey"), Direction: text("direction")}
	if g, ok := props["gap"].(float64); ok && g >= 0 && g < 16 {
		e.Gap = int(g)
	} else if g, ok := props["gap"].(int64); ok && g >= 0 && g < 16 {
		e.Gap = int(g)
	}
	if e.Label == "" && (typ == "Button" || typ == "Link") {
		e.Label = e.Text
	}
	if e.Text == "" && typ != "Box" {
		e.Text = e.Label
	}
	switch c := props["children"].(type) {
	case []any:
		for _, child := range c {
			if ce, ok := h.element(child, depth+1); ok {
				e.Children = append(e.Children, ce)
			}
		}
	case nil:
	default:
		if ce, ok := h.element(c, depth+1); ok {
			e.Children = append(e.Children, ce)
		}
	}
	if typ == "Button" {
		e.press = h.pressFunc(props["onPress"])
	}
	return e, true
}

// pressFunc turns an onPress function token back into a call into the mod
// that made it.
func (h *Host) pressFunc(v any) func() {
	token, ok := fnToken(v)
	if !ok {
		return nil
	}
	key := token[:max(0, strings.LastIndex(token, "#"))]
	for _, r := range h.snapshot() {
		if r.mod.Key() == key {
			return func() {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
					defer cancel()
					_, _ = r.callToken(ctx, token)
				}()
			}
		}
	}
	return nil
}

func fnToken(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return "", false
	}
	s, ok := m[fnKey].(string)
	return s, ok
}
