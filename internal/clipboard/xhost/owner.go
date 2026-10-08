package xhost

import (
	"encoding/binary"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

const incrThreshold = 200000
const incrChunk = 65536

// Owner owns the CLIPBOARD and PRIMARY selections of one X server and serves
// the one item it holds to whichever program asks, as xclip or any toolkit
// clipboard would.
type Owner struct {
	x      *xgb.Conn
	win    xproto.Window
	atoms  map[string]xproto.Atom
	names  map[xproto.Atom]string
	mu     sync.Mutex
	mime   string
	data   []byte
	incrs  map[incrKey]*incrState
	closed chan struct{}
}

type incrKey struct {
	win  xproto.Window
	prop xproto.Atom
}

type incrState struct{ off int }

// NewOwner connects to display (with XAUTHORITY already in the environment)
// and prepares the owner window.
func NewOwner(display string) (*Owner, error) {
	x, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, err
	}
	o := &Owner{x: x, atoms: map[string]xproto.Atom{}, names: map[xproto.Atom]string{}, incrs: map[incrKey]*incrState{}, closed: make(chan struct{})}
	screen := xproto.Setup(x).DefaultScreen(x)
	o.win, err = xproto.NewWindowId(x)
	if err != nil {
		return nil, err
	}
	if err := xproto.CreateWindowChecked(x, 0, o.win, screen.Root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOnly, screen.RootVisual, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange}).Check(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *Owner) atom(name string) xproto.Atom {
	if a, ok := o.atoms[name]; ok {
		return a
	}
	r, err := xproto.InternAtom(o.x, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0
	}
	o.atoms[name] = r.Atom
	o.names[r.Atom] = name
	return r.Atom
}

func (o *Owner) name(a xproto.Atom) string {
	if n, ok := o.names[a]; ok {
		return n
	}
	r, err := xproto.GetAtomName(o.x, a).Reply()
	if err != nil {
		return ""
	}
	o.names[a] = r.Name
	o.atoms[r.Name] = a
	return r.Name
}

// Set replaces the held item and claims both selections. An empty mime or no
// data clears it (the selections are released).
func (o *Owner) Set(mime string, data []byte) {
	o.mu.Lock()
	o.mime, o.data = mime, append([]byte(nil), data...)
	o.incrs = map[incrKey]*incrState{}
	o.mu.Unlock()
	owner := o.win
	if len(data) == 0 {
		owner = 0
	}
	for _, sel := range []string{"CLIPBOARD", "PRIMARY"} {
		xproto.SetSelectionOwner(o.x, owner, o.atom(sel), xproto.TimeCurrentTime)
	}
}

// Close disconnects.
func (o *Owner) Close() { o.x.Close() }

func (o *Owner) targets() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	t := []string{"TARGETS", "TIMESTAMP"}
	if o.mime == "text/plain" {
		return append(t, "UTF8_STRING", "STRING", "TEXT", "text/plain", "text/plain;charset=utf-8")
	}
	if o.mime != "" {
		return append(t, o.mime)
	}
	return t
}

func (o *Owner) typeServes(target string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.mime == "" {
		return false
	}
	if o.mime == "text/plain" {
		switch target {
		case "UTF8_STRING", "STRING", "TEXT", "text/plain", "text/plain;charset=utf-8":
			return true
		}
		return false
	}
	return target == o.mime
}

// Run serves selection requests until the connection closes.
func (o *Owner) Run() {
	defer close(o.closed)
	for {
		ev, err := o.x.WaitForEvent()
		if ev == nil && err == nil {
			return
		}
		switch e := ev.(type) {
		case xproto.SelectionRequestEvent:
			o.request(e)
		case xproto.PropertyNotifyEvent:
			if e.State == xproto.PropertyDelete {
				o.nextChunk(e.Window, e.Atom)
			}
		}
	}
}

func (o *Owner) notify(e xproto.SelectionRequestEvent, prop xproto.Atom) {
	n := xproto.SelectionNotifyEvent{Time: e.Time, Requestor: e.Requestor, Selection: e.Selection, Target: e.Target, Property: prop}
	xproto.SendEvent(o.x, false, e.Requestor, 0, string(n.Bytes()))
}

func (o *Owner) request(e xproto.SelectionRequestEvent) {
	prop := e.Property
	if prop == 0 {
		prop = e.Target
	}
	target := o.name(e.Target)
	switch {
	case target == "TARGETS":
		var b []byte
		for _, t := range o.targets() {
			b = binary.LittleEndian.AppendUint32(b, uint32(o.atom(t)))
		}
		xproto.ChangeProperty(o.x, xproto.PropModeReplace, e.Requestor, prop, xproto.AtomAtom, 32, uint32(len(b)/4), b)
		o.notify(e, prop)
	case target == "TIMESTAMP":
		b := binary.LittleEndian.AppendUint32(nil, uint32(e.Time))
		xproto.ChangeProperty(o.x, xproto.PropModeReplace, e.Requestor, prop, xproto.AtomInteger, 32, 1, b)
		o.notify(e, prop)
	case o.typeServes(target):
		o.mu.Lock()
		data := o.data
		o.mu.Unlock()
		typ := e.Target
		if o.name(e.Target) == "TEXT" {
			typ = o.atom("UTF8_STRING")
		}
		if len(data) <= incrThreshold {
			xproto.ChangeProperty(o.x, xproto.PropModeReplace, e.Requestor, prop, typ, 8, uint32(len(data)), data)
			o.notify(e, prop)
			return
		}
		// INCR: announce the size, then hand over a chunk each time the
		// requestor deletes the property.
		xproto.ChangeWindowAttributes(o.x, e.Requestor, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange})
		size := binary.LittleEndian.AppendUint32(nil, uint32(len(data)))
		o.mu.Lock()
		o.incrs[incrKey{e.Requestor, prop}] = &incrState{}
		o.mu.Unlock()
		xproto.ChangeProperty(o.x, xproto.PropModeReplace, e.Requestor, prop, o.atom("INCR"), 32, 1, size)
		o.notify(e, prop)
	default:
		o.notify(e, 0)
	}
}

func (o *Owner) nextChunk(win xproto.Window, prop xproto.Atom) {
	o.mu.Lock()
	st := o.incrs[incrKey{win, prop}]
	if st == nil {
		o.mu.Unlock()
		return
	}
	data, mime := o.data, o.mime
	end := st.off + incrChunk
	if end > len(data) {
		end = len(data)
	}
	chunk := data[st.off:end]
	if len(chunk) == 0 {
		delete(o.incrs, incrKey{win, prop})
	}
	st.off = end
	o.mu.Unlock()
	typ := o.atom("UTF8_STRING")
	if mime != "text/plain" {
		typ = o.atom(mime)
	}
	xproto.ChangeProperty(o.x, xproto.PropModeReplace, win, prop, typ, 8, uint32(len(chunk)), chunk)
}
