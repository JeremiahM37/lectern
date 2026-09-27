package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"sync"
	"time"
)

// Tabs is one browser with its pages: the tab strip the Browser pane shows
// and the agent's tools address by id. A page a tab opens (a target=_blank
// link, window.open) becomes a tab of its own. Downloads from any tab land in
// one directory on the browser's machine.
type Tabs struct {
	conn *Conn
	vp   Viewport

	mu        sync.Mutex
	pages     map[int]*Browser
	byTarget  map[string]int
	next      int
	active    int
	subs      map[int]chan struct{}
	nextSub   int
	downloads []*Download
	dlDir     string
	closing   bool
	// OnDownload runs when a download finishes and returns where the file
	// ended up. The browser saves each one under its id, so two files with
	// one name never overwrite each other; this gives it its name back.
	OnDownload func(d Download) string
}

// TabInfo describes one tab.
type TabInfo struct {
	ID     int    `json:"id"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
}

// Download is one file a page downloaded.
type Download struct {
	GUID     string  `json:"-"`
	URL      string  `json:"url"`
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	State    string  `json:"state"` // inProgress | completed | canceled
	Received int64   `json:"received"`
	Total    int64   `json:"total"`
	Time     float64 `json:"time"`
}

// OpenTabs connects to a started browser and opens its first tab. Downloads
// go to downloadDir, which must be an absolute path on the browser's machine;
// "" leaves them refused.
func OpenTabs(ctx context.Context, dial Dial, proc *Process, vp Viewport, downloadDir string) (*Tabs, error) {
	conn, err := connect(ctx, dial, proc.Port, proc.Path)
	if err != nil {
		return nil, err
	}
	t := &Tabs{conn: conn, vp: vp.normal(), pages: map[int]*Browser{}, byTarget: map[string]int{}, subs: map[int]chan struct{}{}}
	conn.handle("", t.event)
	if err := conn.Call(ctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		conn.Close()
		return nil, err
	}
	behavior := map[string]any{"behavior": "deny"}
	if downloadDir != "" && path.IsAbs(downloadDir) {
		t.dlDir = downloadDir
		behavior = map[string]any{"behavior": "allowAndName", "downloadPath": downloadDir, "eventsEnabled": true}
	}
	if err := conn.Call(ctx, "", "Browser.setDownloadBehavior", behavior, nil); err != nil {
		conn.Close()
		return nil, err
	}
	if _, _, err := t.New(ctx, ""); err != nil {
		conn.Close()
		return nil, err
	}
	return t, nil
}

// Close ends the connection; the process is stopped separately.
func (t *Tabs) Close() {
	t.mu.Lock()
	t.closing = true
	t.mu.Unlock()
	t.conn.Close()
}

// Done is closed when the browser goes away.
func (t *Tabs) Done() <-chan struct{} { return t.conn.Done() }

func (t *Tabs) add(b *Browser, activate bool) int {
	t.mu.Lock()
	t.next++
	id := t.next
	t.pages[id] = b
	t.byTarget[b.targetID] = id
	if activate || t.active == 0 {
		t.active = id
	}
	t.mu.Unlock()
	t.notify()
	return id
}

// New opens a tab, at address when given, and makes it the active one.
func (t *Tabs) New(ctx context.Context, address string) (int, *Browser, error) {
	if address != "" {
		if _, err := CheckURL(address); err != nil {
			return 0, nil, err
		}
	}
	t.mu.Lock()
	vp := t.vp
	t.mu.Unlock()
	b, err := openPage(ctx, t.conn, vp, "")
	if err != nil {
		return 0, nil, err
	}
	id := t.add(b, true)
	if address != "" {
		if _, err := b.Navigate(ctx, address); err != nil {
			return id, b, err
		}
	}
	return id, b, nil
}

// Get returns tab id, or the active tab for 0.
func (t *Tabs) Get(id int) (*Browser, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if id == 0 {
		id = t.active
	}
	b := t.pages[id]
	if b == nil {
		return nil, 0, fmt.Errorf("no tab %d; list the tabs to see which are open", id)
	}
	return b, id, nil
}

// Select makes a tab the active one: the one the pane shows and tools use by
// default.
func (t *Tabs) Select(id int) error {
	t.mu.Lock()
	if t.pages[id] == nil {
		t.mu.Unlock()
		return fmt.Errorf("no tab %d", id)
	}
	t.active = id
	t.mu.Unlock()
	t.notify()
	return nil
}

// CloseTab closes one tab. Closing the last one leaves a blank tab, so there
// is always a page to show.
func (t *Tabs) CloseTab(ctx context.Context, id int) error {
	b, id, err := t.Get(id)
	if err != nil {
		return err
	}
	t.remove(id)
	b.Close()
	t.mu.Lock()
	empty := len(t.pages) == 0
	t.mu.Unlock()
	if empty {
		_, _, err = t.New(ctx, "")
	}
	return err
}

func (t *Tabs) remove(id int) {
	t.mu.Lock()
	b := t.pages[id]
	if b == nil {
		t.mu.Unlock()
		return
	}
	delete(t.pages, id)
	delete(t.byTarget, b.targetID)
	if t.active == id {
		t.active = 0
		for other := range t.pages {
			if other > t.active {
				t.active = other
			}
		}
	}
	t.mu.Unlock()
	t.notify()
}

// List reports the open tabs in the order they were opened.
func (t *Tabs) List() []TabInfo {
	t.mu.Lock()
	ids := make([]int, 0, len(t.pages))
	for id := range t.pages {
		ids = append(ids, id)
	}
	active := t.active
	pages := t.pages
	t.mu.Unlock()
	sort.Ints(ids)
	out := make([]TabInfo, 0, len(ids))
	for _, id := range ids {
		st := pages[id].State()
		out = append(out, TabInfo{ID: id, URL: st.URL, Title: st.Title, Active: id == active})
	}
	return out
}

// Active is the active tab's id.
func (t *Tabs) Active() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active
}

// Downloads lists this browser's downloads, newest last.
func (t *Tabs) Downloads() []Download {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Download, 0, len(t.downloads))
	for _, d := range t.downloads {
		out = append(out, *d)
	}
	return out
}

// Resize sets the viewport of every tab, and of tabs opened later.
func (t *Tabs) Resize(ctx context.Context, vp Viewport) error {
	t.mu.Lock()
	t.vp = vp.normal()
	pages := make([]*Browser, 0, len(t.pages))
	for _, b := range t.pages {
		pages = append(pages, b)
	}
	t.mu.Unlock()
	for _, b := range pages {
		if _, err := b.Resize(ctx, vp); err != nil {
			return err
		}
	}
	return nil
}

// Changes signals whenever tabs open, close or change which is active, or a
// download moves.
func (t *Tabs) Changes() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	t.mu.Lock()
	t.nextSub++
	id := t.nextSub
	t.subs[id] = ch
	t.mu.Unlock()
	return ch, func() {
		t.mu.Lock()
		delete(t.subs, id)
		t.mu.Unlock()
	}
}

func (t *Tabs) notify() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, ch := range t.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// event handles the browser-level events: pages opening and closing, and
// downloads.
func (t *Tabs) event(ev Event) {
	switch ev.Method {
	case "Target.targetCreated":
		var p struct {
			Info struct {
				TargetID string `json:"targetId"`
				Type     string `json:"type"`
				OpenerID string `json:"openerId"`
			} `json:"targetInfo"`
		}
		if json.Unmarshal(ev.Params, &p) != nil || p.Info.Type != "page" || p.Info.OpenerID == "" {
			return
		}
		t.mu.Lock()
		_, ours := t.byTarget[p.Info.OpenerID]
		vp := t.vp
		t.mu.Unlock()
		if !ours {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			b, err := openPage(ctx, t.conn, vp, p.Info.TargetID)
			if err != nil {
				return
			}
			t.add(b, true)
			b.Sync(ctx)
		}()
	case "Target.targetDestroyed":
		var p struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(ev.Params, &p) != nil {
			return
		}
		t.mu.Lock()
		id, ok := t.byTarget[p.TargetID]
		closing := t.closing
		t.mu.Unlock()
		if ok && !closing {
			// The page closed itself (window.close()).
			t.remove(id)
			t.mu.Lock()
			empty := len(t.pages) == 0
			t.mu.Unlock()
			if empty {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					_, _, _ = t.New(ctx, "")
				}()
			}
		}
	case "Browser.downloadWillBegin":
		var p struct {
			GUID string `json:"guid"`
			URL  string `json:"url"`
			Name string `json:"suggestedFilename"`
		}
		if json.Unmarshal(ev.Params, &p) != nil || t.dlDir == "" {
			return
		}
		name := path.Base(p.Name)
		t.mu.Lock()
		t.downloads = append(t.downloads, &Download{GUID: p.GUID, URL: clip(p.URL, 1000), Name: name,
			Path: path.Join(t.dlDir, p.GUID), State: "inProgress", Time: now()})
		if len(t.downloads) > 50 {
			t.downloads = t.downloads[len(t.downloads)-50:]
		}
		t.mu.Unlock()
		t.notify()
	case "Browser.downloadProgress":
		var p struct {
			GUID     string  `json:"guid"`
			Total    float64 `json:"totalBytes"`
			Received float64 `json:"receivedBytes"`
			State    string  `json:"state"`
		}
		if json.Unmarshal(ev.Params, &p) != nil {
			return
		}
		var done *Download
		changed := false
		t.mu.Lock()
		for _, d := range t.downloads {
			if d.GUID == p.GUID {
				changed = d.State != p.State
				d.Total, d.Received, d.State = int64(p.Total), int64(p.Received), p.State
				if changed && p.State == "completed" {
					done = d
				}
			}
		}
		hook := t.OnDownload
		t.mu.Unlock()
		if done != nil && hook != nil {
			go func() {
				t.mu.Lock()
				snap := *done
				t.mu.Unlock()
				if final := hook(snap); final != "" {
					t.mu.Lock()
					done.Path, done.Name = final, path.Base(final)
					t.mu.Unlock()
				}
				t.notify()
			}()
		} else if changed {
			t.notify()
		}
	}
}
