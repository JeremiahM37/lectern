package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/browser"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Design Mode's "Send to agent": what the operator picked in the Browser pane
// becomes real files beside the session's work, through the same staging every
// attachment uses, and one typed message that points at them.

type designElement struct {
	Selector      string            `json:"selector"`
	Breadcrumb    string            `json:"breadcrumb"`
	Tag           string            `json:"tag"`
	Text          string            `json:"text"`
	HTML          string            `json:"html"`
	HTMLTruncated bool              `json:"html_truncated"`
	HTMLLength    int               `json:"html_length"`
	CSS           map[string]string `json:"css"`
	Rules         []string          `json:"rules"`
	Source        *struct {
		File   string `json:"file"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
		Via    string `json:"via"`
	} `json:"source"`
	ContextHTML string       `json:"context_html"`
	Rect        browser.Clip `json:"rect"`
	Scroll      struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"scroll"`
	Viewport struct {
		Width  int     `json:"width"`
		Height int     `json:"height"`
		DPR    float64 `json:"dpr"`
	} `json:"viewport"`
	URL   string `json:"url"`
	Title string `json:"title"`
	// ClientPNG is the pane's own capture (a data: URL), used only when no
	// headless browser can render the page.
	ClientPNG string `json:"client_png"`
}

type designIn struct {
	Note     string          `json:"note"`
	Source   string          `json:"source"` // frame | browser
	ViewID   string          `json:"view_id"`
	Elements []designElement `json:"elements"`
	// Send false stages the files and returns the message without typing it.
	Send *bool `json:"send"`
}

const (
	shotLiveBrowser = "the session's shared browser (live page, via DevTools)"
	shotClient      = "a client-side capture in the Browser pane (approximate: no headless browser could render the page)"
)

func (in *designIn) validate() error {
	if in.Source != "frame" && in.Source != "browser" {
		return invalid("source must be frame or browser")
	}
	if len(in.Elements) == 0 || len(in.Elements) > 8 {
		return invalid("select between 1 and 8 elements")
	}
	if len(in.Note) > 4000 {
		return invalid("the note is too long")
	}
	for i := range in.Elements {
		e := &in.Elements[i]
		if strings.TrimSpace(e.Selector) == "" || len(e.Selector) > 2000 {
			return invalid("element %d has no usable selector", i+1)
		}
		if len(e.HTML) > 20000 || len(e.ContextHTML) > 6000 || len(e.CSS) > 150 || len(e.Rules) > 20 || len(e.ClientPNG) > 12<<20 {
			return invalid("element %d is larger than Design Mode sends", i+1)
		}
		e.Breadcrumb, e.Text, e.Title = clip(e.Breadcrumb, 400), clip(e.Text, 400), clip(e.Title, 200)
		if e.Source != nil {
			if e.Source.File = clip(strings.TrimSpace(e.Source.File), 500); e.Source.File == "" || strings.ContainsAny(e.Source.File, "\n\r") {
				e.Source = nil
			}
		}
	}
	return nil
}

// localURL is where the element's page lives on the target itself: the view's
// own origin swapped for the dev server's localhost.
func localURL(raw string, port int) string {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Sprintf("http://localhost:%d/", port)
	}
	u.Scheme, u.Host, u.User = "http", "localhost:"+strconv.Itoa(port), nil
	q := u.Query()
	q.Del(browser.TicketParam)
	u.RawQuery = q.Encode()
	return u.String()
}

func decodePNG(dataURL string) []byte {
	raw, ok := strings.CutPrefix(dataURL, "data:image/png;base64,")
	if !ok {
		return nil
	}
	png, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(png) < 8 || string(png[1:4]) != "PNG" {
		return nil
	}
	return png
}

type designShot struct {
	png    []byte
	source string
	err    string
}

// renderShots draws each element in a fresh page of the session's browser at
// the same URL and viewport, cropped to the element.
func (s *Server) renderShots(ctx context.Context, sess *store.Session, in *designIn, port int) ([]designShot, string) {
	shots := make([]designShot, len(in.Elements))
	sb, err := s.ensureBrowser(ctx, sess, browser.Viewport{}, "")
	if err != nil {
		return shots, err.Error()
	}
	target, _ := s.DB.Target(sess.TargetID)
	where := "the Lectern host"
	if sb.where == "target" && target != nil {
		where = target.Name
	}
	source := "headless Chromium on " + where + " (a fresh render at the same URL and viewport)"
	var page *browser.Browser
	loaded := ""
	defer func() {
		if page != nil {
			page.Close()
		}
	}()
	for i, e := range in.Elements {
		vp := browser.Viewport{Width: e.Viewport.Width, Height: e.Viewport.Height, Scale: e.Viewport.DPR}
		if page == nil {
			var first *browser.Browser
			if first, _, err = sb.tabs.Get(0); err == nil {
				page, err = first.NewPage(ctx, vp)
			}
			if err != nil {
				return shots, err.Error()
			}
		} else if _, err := page.Resize(ctx, vp); err != nil {
			shots[i].err = err.Error()
			continue
		}
		address := localURL(e.URL, port)
		if address != loaded {
			// A loaded machine can take a moment to answer, or to lay the
			// page out once it has: try a few times before giving up on the
			// render and falling back to the pane's own capture.
			var err error
			for try := 0; try < 3; try++ {
				if _, err = page.Navigate(ctx, address); err == nil {
					break
				}
				settle(ctx)
			}
			if err != nil {
				shots[i].err = err.Error()
				continue
			}
			loaded = address
			settle(ctx)
		}
		var raw json.RawMessage
		var err error
		for try := 0; try < 10; try++ {
			if raw, err = page.DescribeSelector(ctx, e.Selector); !errors.Is(err, browser.ErrNotFound) {
				break
			}
			settle(ctx)
		}
		if err != nil {
			shots[i].err = "the element was not found in a fresh render (" + err.Error() + ")"
			continue
		}
		c, err := clipOf(raw)
		if err == nil {
			shots[i].png, err = page.Screenshot(ctx, c)
		}
		if err != nil {
			shots[i].err = err.Error()
			continue
		}
		shots[i].source = source
	}
	return shots, ""
}

func (s *Server) liveShots(ctx context.Context, sess *store.Session, in *designIn) []designShot {
	shots := make([]designShot, len(in.Elements))
	sb := s.browserFor(sess.ID)
	if sb == nil {
		for i := range shots {
			shots[i].err = "the session browser has closed"
		}
		return shots
	}
	live, _, err := sb.tabs.Get(0)
	if err != nil {
		for i := range shots {
			shots[i].err = err.Error()
		}
		return shots
	}
	live.HidePicker(ctx)
	for i, e := range in.Elements {
		raw, err := live.DescribeSelector(ctx, e.Selector)
		if err == nil {
			var c *browser.Clip
			if c, err = clipOf(raw); err == nil {
				shots[i].png, err = live.Screenshot(ctx, c)
			}
		}
		if err != nil {
			shots[i].err = err.Error()
			continue
		}
		shots[i].source = shotLiveBrowser
	}
	return shots
}

func (s *Server) sendDesign(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	var in designIn
	r.Body = http.MaxBytesReader(w, r.Body, 40<<20)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, 422, "invalid JSON body: %s", err)
		return
	}
	if err := in.validate(); err != nil {
		respondErr(w, err)
		return
	}
	if !s.requireHuman(w, r, "sending a Design Mode selection") {
		return
	}
	if sess.Status == sessions.StatusDead || sess.EndedAt != nil {
		httpError(w, 409, "this session has ended")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	var shots []designShot
	port := 0
	if in.Source == "browser" {
		shots = s.liveShots(ctx, sess, &in)
	} else {
		st := s.browsersInit()
		st.mu.Lock()
		v := st.views[in.ViewID]
		st.mu.Unlock()
		if v == nil || v.sessionID != sess.ID {
			httpError(w, 404, "no such view for this session")
			return
		}
		port = v.view.Port
		var why string
		shots, why = s.renderShots(ctx, sess, &in, port)
		for i := range shots {
			if shots[i].png == nil {
				if why != "" && shots[i].err == "" {
					shots[i].err = why
				}
				if png := decodePNG(in.Elements[i].ClientPNG); png != nil {
					shots[i].png, shots[i].source = png, shotClient
				}
			}
		}
	}
	files := []stagedFile{}
	for i, e := range in.Elements {
		n := i + 1
		files = append(files, stagedFile{Name: fmt.Sprintf("element-%d.html", n), Data: []byte(e.HTML + "\n")})
		if shots[i].png != nil {
			files = append(files, stagedFile{Name: fmt.Sprintf("element-%d.png", n), Data: shots[i].png})
		}
	}
	summary := designSummary(&in, shots, port)
	files = append([]stagedFile{{Name: "design.md", Data: []byte(summary)}}, files...)
	staged, err := s.stageAttachments(ctx, sess.TargetID, sess.Workdir, files)
	if err != nil {
		stageRespond(w, err)
		return
	}
	message := designMessage(&in, staged, shots, port)
	sent := false
	if in.Send == nil || *in.Send {
		if err := s.Sessions.SendText(ctx, sess.ID, message); err != nil {
			httpError(w, 409, "the files are staged but the message could not be sent: %s", err)
			return
		}
		sent = true
	}
	sources := make([]map[string]any, len(shots))
	for i, sh := range shots {
		sources[i] = map[string]any{"source": sh.source, "error": sh.err, "captured": sh.png != nil}
	}
	writeJSON(w, 200, map[string]any{"files": staged, "message": message, "sent": sent, "screenshots": sources})
}

func pageOf(e designElement, port int) string {
	if port > 0 {
		return localURL(e.URL, port)
	}
	return e.URL
}

// designSummary is design.md: everything about each element an agent needs to
// find it in the code and change it.
func designSummary(in *designIn, shots []designShot, port int) string {
	var b strings.Builder
	b.WriteString("# Design Mode selection\n\n")
	if note := strings.TrimSpace(in.Note); note != "" {
		fmt.Fprintf(&b, "Request from the operator:\n\n> %s\n\n", strings.ReplaceAll(note, "\n", "\n> "))
	}
	for i, e := range in.Elements {
		n := i + 1
		fmt.Fprintf(&b, "## Element %d: `%s`\n\n", n, e.Tag)
		fmt.Fprintf(&b, "- Page: %s", pageOf(e, port))
		if e.Title != "" {
			fmt.Fprintf(&b, " (%q)", e.Title)
		}
		fmt.Fprintf(&b, "\n- Viewport: %dx%d CSS px, device pixel ratio %g\n", e.Viewport.Width, e.Viewport.Height, e.Viewport.DPR)
		fmt.Fprintf(&b, "- Selector: `%s`\n- DOM path: %s\n", e.Selector, e.Breadcrumb)
		fmt.Fprintf(&b, "- Box: x=%.0f y=%.0f, %.0fx%.0f (relative to the viewport)\n", e.Rect.X, e.Rect.Y, e.Rect.Width, e.Rect.Height)
		if e.Text != "" {
			fmt.Fprintf(&b, "- Text: %q\n", e.Text)
		}
		if e.Source != nil {
			fmt.Fprintf(&b, "- Source: %s (from %s dev info)\n", sourceRef(e), e.Source.Via)
		}
		switch {
		case shots[i].png != nil:
			fmt.Fprintf(&b, "- Screenshot: element-%d.png, from %s\n", n, shots[i].source)
		case shots[i].err != "":
			fmt.Fprintf(&b, "- Screenshot: none (%s)\n", shots[i].err)
		}
		b.WriteString("- Markup: element-" + strconv.Itoa(n) + ".html")
		if e.HTMLTruncated {
			fmt.Fprintf(&b, " (trimmed from %d characters)", e.HTMLLength)
		}
		b.WriteString("\n\n")
		if len(e.CSS) > 0 {
			b.WriteString("Computed CSS that differs from the element's defaults:\n\n```css\n")
			keys := make([]string, 0, len(e.CSS))
			for k := range e.CSS {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, "%s: %s;\n", k, clip(e.CSS[k], 300))
			}
			b.WriteString("```\n\n")
		}
		if e.ContextHTML != "" {
			b.WriteString("Surrounding markup (its parent, trimmed):\n\n```html\n" + e.ContextHTML + "\n```\n\n")
		}
		if len(e.Rules) > 0 {
			b.WriteString("Stylesheet rules that match it:\n\n```css\n")
			for _, rule := range e.Rules {
				b.WriteString(clip(rule, 600) + "\n")
			}
			b.WriteString("```\n\n")
		}
	}
	return b.String()
}

func sourceRef(e designElement) string {
	if e.Source.Line > 0 {
		return fmt.Sprintf("%s:%d", e.Source.File, e.Source.Line)
	}
	return e.Source.File
}

func designMessage(in *designIn, staged []stagedFile, shots []designShot, port int) string {
	var b strings.Builder
	if note := strings.TrimSpace(in.Note); note != "" {
		b.WriteString(note + "\n\n")
	}
	e0 := in.Elements[0]
	count := "1 element"
	if len(in.Elements) > 1 {
		count = fmt.Sprintf("%d elements", len(in.Elements))
	}
	fmt.Fprintf(&b, "Design Mode: I picked %s on %s (viewport %dx%d).\n", count, pageOf(e0, port), e0.Viewport.Width, e0.Viewport.Height)
	for i, e := range in.Elements {
		fmt.Fprintf(&b, "%d. %s (selector: %s)", i+1, clip(e.Breadcrumb, 160), clip(e.Selector, 200))
		if e.Source != nil {
			b.WriteString(", source " + sourceRef(e))
		}
		b.WriteString("\n")
	}
	var pngs []string
	summary := ""
	for _, f := range staged {
		switch {
		case f.Name == "design.md":
			summary = f.Path
		case strings.HasSuffix(f.Name, ".png"):
			pngs = append(pngs, f.Path)
		}
	}
	fmt.Fprintf(&b, "Details (trimmed HTML, non-default CSS, matched rules): %s\n", summary)
	if len(pngs) > 0 {
		src := ""
		for _, sh := range shots {
			if sh.source != "" {
				src = sh.source
				break
			}
		}
		fmt.Fprintf(&b, "Screenshots: %s (from %s)\n", strings.Join(pngs, ", "), src)
	}
	return strings.TrimRight(b.String(), "\n")
}
