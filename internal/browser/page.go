package browser

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

//go:embed picker.js
var pickerJS string

// PickerScript is Design Mode's element picker. mode is "cdp" (it reports
// through a DevTools binding) or "frame" (it reports to parentOrigin with
// postMessage, from inside the Browser pane's iframe).
func PickerScript(mode, parentOrigin string) string {
	cfg, _ := json.Marshal(map[string]string{"mode": mode, "parentOrigin": parentOrigin})
	return "window.__lecternDesignConfig=" + string(cfg) + ";\n" + pickerJS
}

// Viewport is the page size the browser renders at, in CSS pixels.
type Viewport struct {
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Mobile bool    `json:"mobile"`
	Scale  float64 `json:"scale"`
}

func (v Viewport) normal() Viewport {
	if v.Width < 240 || v.Width > 3840 {
		v.Width = 1280
	}
	if v.Height < 240 || v.Height > 2160 {
		v.Height = 800
	}
	if v.Scale < 1 || v.Scale > 3 {
		v.Scale = 1
	}
	return v
}

// LogEntry is one console message or uncaught exception.
type LogEntry struct {
	Time  float64 `json:"time"`
	Level string  `json:"level"`
	Text  string  `json:"text"`
	URL   string  `json:"url,omitempty"`
}

// NetEntry is one request the page made.
type NetEntry struct {
	Time     float64 `json:"time"`
	ID       string  `json:"-"`
	Method   string  `json:"method"`
	URL      string  `json:"url"`
	Type     string  `json:"type,omitempty"`
	Status   int     `json:"status,omitempty"`
	MimeType string  `json:"mime_type,omitempty"`
	Failed   string  `json:"failed,omitempty"`
}

// Frame is one screencast image. PageScale is the page's zoom when a phone
// viewport shows a page laid out wider (one with no viewport meta tag):
// input coordinates are layout pixels, the picture is zoomed by this much.
type Frame struct {
	JPEG      []byte
	Width     int
	Height    int
	PageScale float64
}

// Update is what a watcher hears besides frames: page state changes and
// Design Mode selections.
type Update struct {
	Kind   string          `json:"kind"` // state | design
	URL    string          `json:"url,omitempty"`
	Title  string          `json:"title,omitempty"`
	Design json.RawMessage `json:"design,omitempty"`
}

const logCap = 300

// Browser is one page of a browser, driven over DevTools. The first page
// owns the connection; NewPage opens more on it.
type Browser struct {
	conn     *Conn
	page     string // flattened page session id
	targetID string
	owner    bool

	mu        sync.Mutex
	url       string
	title     string
	vp        Viewport
	console   []LogEntry
	network   []NetEntry
	refs      map[int]int64
	nextRef   int
	loads     []chan struct{}
	frames    map[int]chan Frame
	updates   map[int]chan Update
	nextSub   int
	casting   bool
	design    bool
	designID  string
	lastFrame *Frame
}

// Open connects to a started browser and opens its page.
func Open(ctx context.Context, dial Dial, proc *Process, vp Viewport) (*Browser, error) {
	conn, err := connect(ctx, dial, proc.Port, proc.Path)
	if err != nil {
		return nil, err
	}
	b, err := openPage(ctx, conn, vp)
	if err != nil {
		conn.Close()
		return nil, err
	}
	b.owner = true
	return b, nil
}

// NewPage opens a separate page in the same browser, e.g. for a fresh render
// that must not disturb the page someone is watching.
func (b *Browser) NewPage(ctx context.Context, vp Viewport) (*Browser, error) {
	return openPage(ctx, b.conn, vp)
}

func openPage(ctx context.Context, conn *Conn, vp Viewport) (*Browser, error) {
	b := &Browser{conn: conn, refs: map[int]int64{}, frames: map[int]chan Frame{}, updates: map[int]chan Update{}, vp: vp.normal()}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := conn.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
		return nil, err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": created.TargetID, "flatten": true}, &attached); err != nil {
		return nil, err
	}
	b.page, b.targetID = attached.SessionID, created.TargetID
	conn.handle(b.page, b.event)
	fail := func(err error) (*Browser, error) {
		b.Close()
		return nil, err
	}
	for _, m := range []string{"Page.enable", "Runtime.enable", "Network.enable", "Log.enable", "DOM.enable"} {
		if err := conn.Call(ctx, b.page, m, nil, nil); err != nil {
			return fail(err)
		}
	}
	if err := b.applyViewport(ctx); err != nil {
		return fail(err)
	}
	return b, nil
}

// Close ends the page, and the connection if this page owns it.
func (b *Browser) Close() {
	b.conn.handle(b.page, nil)
	if b.owner {
		b.conn.Close()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = b.conn.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": b.targetID}, nil)
}

// Done is closed when the browser goes away.
func (b *Browser) Done() <-chan struct{} { return b.conn.Done() }

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func (b *Browser) event(ev Event) {
	if ev.SessionID != b.page {
		return
	}
	switch ev.Method {
	case "Page.frameNavigated":
		var p struct {
			Frame struct {
				ParentID string `json:"parentId"`
				URL      string `json:"url"`
				Fragment string `json:"urlFragment"`
			} `json:"frame"`
		}
		if json.Unmarshal(ev.Params, &p) == nil && p.Frame.ParentID == "" {
			b.mu.Lock()
			b.url = p.Frame.URL + p.Frame.Fragment
			b.refs = map[int]int64{}
			b.mu.Unlock()
			b.publish(Update{Kind: "state", URL: p.Frame.URL + p.Frame.Fragment})
		}
	case "Page.navigatedWithinDocument":
		var p struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			b.mu.Lock()
			b.url = p.URL
			b.mu.Unlock()
			b.publish(Update{Kind: "state", URL: p.URL})
		}
	case "Page.loadEventFired":
		b.mu.Lock()
		waiters := b.loads
		b.loads = nil
		b.mu.Unlock()
		for _, ch := range waiters {
			close(ch)
		}
		go b.refreshTitle()
	case "Page.screencastFrame":
		var p struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
			Metadata  struct {
				DeviceWidth     float64 `json:"deviceWidth"`
				DeviceHeight    float64 `json:"deviceHeight"`
				PageScaleFactor float64 `json:"pageScaleFactor"`
			} `json:"metadata"`
		}
		if json.Unmarshal(ev.Params, &p) != nil {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = b.conn.Call(ctx, b.page, "Page.screencastFrameAck", map[string]any{"sessionId": p.SessionID}, nil)
		}()
		data, err := base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			return
		}
		b.sendFrame(Frame{JPEG: data, Width: int(p.Metadata.DeviceWidth), Height: int(p.Metadata.DeviceHeight),
			PageScale: p.Metadata.PageScaleFactor})
	case "Runtime.consoleAPICalled":
		var p struct {
			Type string `json:"type"`
			Args []struct {
				Value       any    `json:"value"`
				Description string `json:"description"`
				Type        string `json:"type"`
			} `json:"args"`
		}
		if json.Unmarshal(ev.Params, &p) != nil {
			return
		}
		parts := make([]string, 0, len(p.Args))
		for _, a := range p.Args {
			switch {
			case a.Type == "string":
				parts = append(parts, fmt.Sprint(a.Value))
			case a.Value != nil:
				raw, _ := json.Marshal(a.Value)
				parts = append(parts, string(raw))
			default:
				parts = append(parts, a.Description)
			}
		}
		b.log(LogEntry{Time: now(), Level: p.Type, Text: clip(strings.Join(parts, " "), 2000)})
	case "Runtime.exceptionThrown":
		var p struct {
			Details struct {
				Text      string `json:"text"`
				URL       string `json:"url"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			text := p.Details.Exception.Description
			if text == "" {
				text = p.Details.Text
			}
			b.log(LogEntry{Time: now(), Level: "exception", Text: clip(text, 2000), URL: p.Details.URL})
		}
	case "Log.entryAdded":
		var p struct {
			Entry struct {
				Level string `json:"level"`
				Text  string `json:"text"`
				URL   string `json:"url"`
			} `json:"entry"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			b.log(LogEntry{Time: now(), Level: p.Entry.Level, Text: clip(p.Entry.Text, 2000), URL: p.Entry.URL})
		}
	case "Network.requestWillBeSent":
		var p struct {
			RequestID string `json:"requestId"`
			Type      string `json:"type"`
			Request   struct {
				Method string `json:"method"`
				URL    string `json:"url"`
			} `json:"request"`
		}
		if json.Unmarshal(ev.Params, &p) == nil && !strings.HasPrefix(p.Request.URL, "data:") {
			b.mu.Lock()
			b.network = append(b.network, NetEntry{Time: now(), ID: p.RequestID, Method: p.Request.Method,
				URL: clip(p.Request.URL, 1000), Type: p.Type})
			if len(b.network) > logCap {
				b.network = b.network[len(b.network)-logCap:]
			}
			b.mu.Unlock()
		}
	case "Network.responseReceived":
		var p struct {
			RequestID string `json:"requestId"`
			Response  struct {
				Status   int    `json:"status"`
				MimeType string `json:"mimeType"`
			} `json:"response"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			b.updateNet(p.RequestID, func(e *NetEntry) { e.Status, e.MimeType = p.Response.Status, p.Response.MimeType })
		}
	case "Network.loadingFailed":
		var p struct {
			RequestID string `json:"requestId"`
			ErrorText string `json:"errorText"`
			Canceled  bool   `json:"canceled"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			b.updateNet(p.RequestID, func(e *NetEntry) {
				e.Failed = p.ErrorText
				if p.Canceled && e.Failed == "" {
					e.Failed = "canceled"
				}
			})
		}
	case "Runtime.bindingCalled":
		var p struct {
			Name    string `json:"name"`
			Payload string `json:"payload"`
		}
		if json.Unmarshal(ev.Params, &p) == nil && p.Name == designBinding && len(p.Payload) < 1<<20 && json.Valid([]byte(p.Payload)) {
			b.mu.Lock()
			on := b.design
			b.mu.Unlock()
			if on {
				b.publish(Update{Kind: "design", Design: json.RawMessage(p.Payload)})
			}
		}
	}
}

const designBinding = "__lecternDesignEmit"

func (b *Browser) log(e LogEntry) {
	b.mu.Lock()
	b.console = append(b.console, e)
	if len(b.console) > logCap {
		b.console = b.console[len(b.console)-logCap:]
	}
	b.mu.Unlock()
}

func (b *Browser) updateNet(id string, f func(*NetEntry)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.network) - 1; i >= 0; i-- {
		if b.network[i].ID == id {
			f(&b.network[i])
			return
		}
	}
}

func (b *Browser) refreshTitle() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if b.conn.Call(ctx, b.page, "Runtime.evaluate", map[string]any{"expression": "document.title", "returnByValue": true}, &res) == nil {
		b.mu.Lock()
		b.title = res.Result.Value
		u := b.url
		b.mu.Unlock()
		b.publish(Update{Kind: "state", URL: u, Title: res.Result.Value})
	}
}

// State is the page as it stands.
type State struct {
	URL      string   `json:"url"`
	Title    string   `json:"title"`
	Viewport Viewport `json:"viewport"`
	Design   bool     `json:"design"`
}

// State reports the current page.
func (b *Browser) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return State{URL: b.url, Title: b.title, Viewport: b.vp, Design: b.design}
}

// ---- watchers -----------------------------------------------------------------

// Watch subscribes to screencast frames and updates until the returned stop
// function runs. The first watcher starts the screencast; the last one out
// stops it, so an unwatched browser costs no encoding.
func (b *Browser) Watch(ctx context.Context) (<-chan Frame, <-chan Update, func()) {
	frames, updates := make(chan Frame, 1), make(chan Update, 16)
	b.mu.Lock()
	b.nextSub++
	id := b.nextSub
	b.frames[id], b.updates[id] = frames, updates
	start := !b.casting
	b.casting = true
	last := b.lastFrame
	b.mu.Unlock()
	if last != nil {
		frames <- *last
	}
	if start {
		_ = b.startCast(ctx)
	}
	go b.capture(ctx) // a still page sends no screencast frames at all
	var once sync.Once
	return frames, updates, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.frames, id)
			delete(b.updates, id)
			stop := len(b.frames) == 0 && b.casting
			if stop {
				b.casting = false
			}
			b.mu.Unlock()
			if stop {
				c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = b.conn.Call(c, b.page, "Page.stopScreencast", nil, nil)
			}
		})
	}
}

func (b *Browser) startCast(ctx context.Context) error {
	b.mu.Lock()
	vp := b.vp
	b.mu.Unlock()
	// Frames travel over the relay too, which carries at most 1 MiB a message:
	// a phone's 3x density is capped at 1.5x, which still reads sharply.
	scale := min(vp.Scale, 1.5)
	return b.conn.Call(ctx, b.page, "Page.startScreencast", map[string]any{"format": "jpeg", "quality": 70,
		"maxWidth": int(float64(vp.Width) * scale), "maxHeight": int(float64(vp.Height) * scale), "everyNthFrame": 1}, nil)
}

// capture sends one frame now, for a watcher that just arrived.
func (b *Browser) capture(ctx context.Context) {
	var shot struct {
		Data string `json:"data"`
	}
	if err := b.conn.Call(ctx, b.page, "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 70}, &shot); err != nil {
		return
	}
	data, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil {
		return
	}
	b.mu.Lock()
	vp := b.vp
	b.mu.Unlock()
	b.sendFrame(Frame{JPEG: data, Width: vp.Width, Height: vp.Height})
}

func (b *Browser) sendFrame(f Frame) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if f.PageScale <= 0 {
		f.PageScale = 1
		if b.lastFrame != nil {
			f.PageScale = b.lastFrame.PageScale
		}
	}
	b.lastFrame = &f
	for _, ch := range b.frames {
		// Latest wins: a slow watcher skips frames rather than lagging behind.
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- f:
		default:
		}
	}
}

func (b *Browser) publish(u Update) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.updates {
		select {
		case ch <- u:
		default:
		}
	}
}

// ---- navigation ---------------------------------------------------------------

// CheckURL accepts only web addresses: the browser is for pages, not for
// reading the machine's files.
func CheckURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "about:blank" {
		return raw, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("the address must be an http(s) URL")
	}
	return u.String(), nil
}

func (b *Browser) loadWaiter() chan struct{} {
	ch := make(chan struct{})
	b.mu.Lock()
	b.loads = append(b.loads, ch)
	b.mu.Unlock()
	return ch
}

func (b *Browser) waitLoad(ctx context.Context, ch chan struct{}, limit time.Duration) bool {
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// Navigate loads an address and waits for it to finish loading, up to 15s.
func (b *Browser) Navigate(ctx context.Context, raw string) (State, error) {
	address, err := CheckURL(raw)
	if err != nil {
		return State{}, err
	}
	wait := b.loadWaiter()
	var res struct {
		ErrorText string `json:"errorText"`
	}
	if err := b.conn.Call(ctx, b.page, "Page.navigate", map[string]any{"url": address}, &res); err != nil {
		return State{}, err
	}
	if res.ErrorText != "" {
		return b.State(), fmt.Errorf("could not load %s: %s", address, res.ErrorText)
	}
	b.waitLoad(ctx, wait, 15*time.Second)
	b.refreshTitle()
	return b.State(), nil
}

// History moves back (-1) or forward (+1).
func (b *Browser) History(ctx context.Context, delta int) (State, error) {
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := b.conn.Call(ctx, b.page, "Page.getNavigationHistory", nil, &h); err != nil {
		return State{}, err
	}
	i := h.CurrentIndex + delta
	if i < 0 || i >= len(h.Entries) {
		return b.State(), nil
	}
	wait := b.loadWaiter()
	if err := b.conn.Call(ctx, b.page, "Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[i].ID}, nil); err != nil {
		return State{}, err
	}
	b.waitLoad(ctx, wait, 10*time.Second)
	b.refreshTitle()
	return b.State(), nil
}

// Reload reloads the page.
func (b *Browser) Reload(ctx context.Context) (State, error) {
	wait := b.loadWaiter()
	if err := b.conn.Call(ctx, b.page, "Page.reload", map[string]any{}, nil); err != nil {
		return State{}, err
	}
	b.waitLoad(ctx, wait, 15*time.Second)
	b.refreshTitle()
	return b.State(), nil
}

// Resize changes the viewport, e.g. to a phone's size.
func (b *Browser) Resize(ctx context.Context, vp Viewport) (State, error) {
	b.mu.Lock()
	b.vp = vp.normal()
	casting := b.casting
	b.mu.Unlock()
	if err := b.applyViewport(ctx); err != nil {
		return State{}, err
	}
	if casting {
		_ = b.conn.Call(ctx, b.page, "Page.stopScreencast", nil, nil)
		_ = b.startCast(ctx)
		go b.capture(context.Background())
	}
	return b.State(), nil
}

func (b *Browser) applyViewport(ctx context.Context) error {
	b.mu.Lock()
	vp := b.vp
	b.mu.Unlock()
	if err := b.conn.Call(ctx, b.page, "Emulation.setDeviceMetricsOverride", map[string]any{"width": vp.Width,
		"height": vp.Height, "deviceScaleFactor": vp.Scale, "mobile": vp.Mobile}, nil); err != nil {
		return err
	}
	return b.conn.Call(ctx, b.page, "Emulation.setTouchEmulationEnabled", map[string]any{"enabled": vp.Mobile}, nil)
}

// ---- reading ------------------------------------------------------------------

// Console returns the recent console messages, oldest first.
func (b *Browser) Console() []LogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]LogEntry{}, b.console...)
}

// Network returns the recent requests, oldest first.
func (b *Browser) Network() []NetEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]NetEntry{}, b.network...)
}

// Clip is a rectangle in document CSS pixels.
type Clip struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Screenshot captures the viewport, or clip when given, as PNG.
func (b *Browser) Screenshot(ctx context.Context, clip *Clip) ([]byte, error) {
	params := map[string]any{"format": "png"}
	if clip != nil {
		if clip.Width < 1 || clip.Height < 1 {
			return nil, fmt.Errorf("the element has no visible size")
		}
		params["clip"] = map[string]any{"x": clip.X, "y": clip.Y, "width": clip.Width, "height": clip.Height, "scale": 1}
		params["captureBeyondViewport"] = true
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := b.conn.Call(ctx, b.page, "Page.captureScreenshot", params, &shot); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(shot.Data)
}

// Evaluate runs a read-only expression: DevTools refuses anything with a side
// effect (a write to the DOM, a network call, an assignment to a global), so
// an agent can inspect the page but not change it this way.
func (b *Browser) Evaluate(ctx context.Context, expression string) (any, error) {
	if len(expression) > 20000 {
		return nil, fmt.Errorf("expression is too long")
	}
	var res struct {
		Result struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := b.conn.Call(ctx, b.page, "Runtime.evaluate", map[string]any{"expression": expression,
		"returnByValue": true, "throwOnSideEffect": true, "timeout": 3000}, &res); err != nil {
		return nil, err
	}
	if res.Exception != nil {
		msg := res.Exception.Exception.Description
		if msg == "" {
			msg = res.Exception.Text
		}
		if strings.Contains(msg, "side-effect") {
			return nil, fmt.Errorf("refused: evaluate is read-only, and DevTools could not prove this expression " +
				"changes nothing. Reads such as document.querySelector('h1').textContent work; use click, fill or press to act")
		}
		return nil, fmt.Errorf("%s", clip(msg, 2000))
	}
	if len(res.Result.Value) > 0 {
		var v any
		if json.Unmarshal(res.Result.Value, &v) == nil {
			return v, nil
		}
	}
	if res.Result.Type == "undefined" {
		return nil, nil
	}
	return res.Result.Description, nil
}

// ---- Design Mode --------------------------------------------------------------

// SetDesign turns the element picker on or off in the live page. While on it
// is also installed into every page the browser loads.
func (b *Browser) SetDesign(ctx context.Context, on bool) error {
	b.mu.Lock()
	prev, id := b.design, b.designID
	b.mu.Unlock()
	if on == prev {
		return nil
	}
	if on {
		if err := b.conn.Call(ctx, b.page, "Runtime.addBinding", map[string]any{"name": designBinding}, nil); err != nil {
			return err
		}
		src := PickerScript("cdp", "")
		var added struct {
			Identifier string `json:"identifier"`
		}
		if err := b.conn.Call(ctx, b.page, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": src}, &added); err != nil {
			return err
		}
		b.mu.Lock()
		b.design, b.designID = true, added.Identifier
		b.mu.Unlock()
		return b.conn.Call(ctx, b.page, "Runtime.evaluate", map[string]any{"expression": src}, nil)
	}
	b.mu.Lock()
	b.design, b.designID = false, ""
	b.mu.Unlock()
	if id != "" {
		_ = b.conn.Call(ctx, b.page, "Page.removeScriptToEvaluateOnNewDocument", map[string]any{"identifier": id}, nil)
	}
	_ = b.conn.Call(ctx, b.page, "Runtime.removeBinding", map[string]any{"name": designBinding}, nil)
	return b.conn.Call(ctx, b.page, "Runtime.evaluate", map[string]any{
		"expression": "window.__lecternDesign && window.__lecternDesign.disable()"}, nil)
}

// DescribeSelector runs the picker's own description of the element at
// selector: the same payload a click in Design Mode produces. It is how a
// fresh render of a page yields the element an operator picked elsewhere.
func (b *Browser) DescribeSelector(ctx context.Context, selector string) (json.RawMessage, error) {
	sel, _ := json.Marshal(selector)
	expr := "(function(){" + PickerScript("describe", "") + "\n;var el=document.querySelector(" + string(sel) +
		");if(!el)return null;el.scrollIntoView({block:'center',inline:'center'});" +
		"return JSON.stringify(window.__lecternDesign.describe(el));})()"
	var res struct {
		Result struct {
			Value *string `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := b.conn.Call(ctx, b.page, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true}, &res); err != nil {
		return nil, err
	}
	if res.Exception != nil {
		return nil, fmt.Errorf("%s", res.Exception.Text)
	}
	if res.Result.Value == nil {
		return nil, ErrNotFound
	}
	return json.RawMessage(*res.Result.Value), nil
}

// ErrNotFound means no element matched.
var ErrNotFound = errors.New("no element matches")

// HidePicker removes the picker's highlight before a screenshot.
func (b *Browser) HidePicker(ctx context.Context) {
	_ = b.conn.Call(ctx, b.page, "Runtime.evaluate", map[string]any{
		"expression": "window.__lecternDesign && window.__lecternDesign.hide()"}, nil)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
