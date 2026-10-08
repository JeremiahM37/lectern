// Package clipboard lets an agent running on the Lectern host read the
// clipboard of the device the person is driving it from.
//
// The agents (Claude Code, Codex) read the clipboard by running wl-paste or
// xclip. A host in a rack has no display, so those find nothing. Each launched
// session gets shims for both on its PATH (shim.go); a shim asks the server
// for "the clipboard of whoever is driving this session", and the server asks
// that person's own client over the connection the client already holds open.
//
// What keeps this from being a way to read a person's clipboard at will is
// spelled out in docs/clipboard.md; the rules live in Ask below:
//
//   - the asker proves it is the session (that session's hook token), and is
//     only ever routed to clients attached to that same session or to a
//     session-less client (the phone app, the dashboard), never to a client
//     attached to another session;
//   - a client is asked only if its person typed, or the client declared
//     itself an explicit bridge, within Window;
//   - one answer at most Timeout long and MaxBytes big, and every request
//     leaves an audit line.
package clipboard

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultWindow is how recently a client must have seen its person type.
	DefaultWindow = 20 * time.Second
	// DefaultTimeout bounds one round trip to a client.
	DefaultTimeout = 3 * time.Second
	// DefaultMaxBytes caps one clipboard payload.
	DefaultMaxBytes = 20 << 20
	// perSessionPerMinute caps how often one session may ask.
	perSessionPerMinute = 30
)

// Operations a shim can ask for.
const (
	OpList = "list" // the MIME types on the clipboard
	OpRead = "read" // the bytes of one type
)

// Errors a caller maps to "unavailable"; the message is for the audit log.
var (
	ErrNoClient    = errors.New("no client of this session is driving it right now")
	ErrTimeout     = errors.New("the client did not answer in time")
	ErrUnavailable = errors.New("the client has nothing of that type on its clipboard")
	ErrTooLarge    = errors.New("the clipboard item is too large")
	ErrRate        = errors.New("too many clipboard requests from this session")
	ErrNotAllowed  = errors.New("not allowed")
)

// Request is what a client is asked.
type Request struct {
	ID   string `json:"id"`
	Op   string `json:"op"`
	Type string `json:"type,omitempty"`
}

// Result is what a client answers.
type Result struct {
	// OK is false when the client has nothing to give (also what a browser,
	// which may not read the clipboard without a gesture, answers).
	OK    bool
	Types []string
	Type  string
	Data  []byte
}

// Provider is one connected client willing to answer clipboard requests.
type Provider struct {
	ID      string
	Session int64 // 0: not attached to a session (phone app, dashboard)
	Kind    string
	Login   string
	// CanRead is false for a client that cannot read a clipboard (a browser):
	// it still counts as the active client, and answers "unavailable" at once.
	CanRead bool
	// Always marks an explicit bridge (`lectern clipboard serve`) whose person
	// cannot be observed typing, for instance over plain SSH.
	Always bool

	connected  time.Time
	lastActive time.Time
	ch         chan Request
}

// Requests is the stream of requests for the provider.
func (p *Provider) Requests() <-chan Request { return p.ch }

type pending struct {
	provider string
	login    string
	done     chan Result
}

// Broker routes clipboard requests between sessions and clients.
type Broker struct {
	Window   time.Duration
	Timeout  time.Duration
	MaxBytes int
	// Audit receives one line per request; may be nil.
	Audit func(fields map[string]any)
	Now   func() time.Time

	mu        sync.Mutex
	providers map[string]*Provider
	pending   map[string]*pending
	asks      map[int64][]time.Time
	seq       uint64
}

// NewBroker returns a broker with the default limits.
func NewBroker() *Broker {
	return &Broker{Window: DefaultWindow, Timeout: DefaultTimeout, MaxBytes: DefaultMaxBytes,
		Now: time.Now, providers: map[string]*Provider{}, pending: map[string]*pending{}, asks: map[int64][]time.Time{}}
}

func (b *Broker) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// Register adds a client. A client re-registering under the same id replaces
// the earlier connection. The returned function removes it.
func (b *Broker) Register(p Provider) (*Provider, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p.connected = b.now()
	p.lastActive = time.Time{}
	p.ch = make(chan Request, 4)
	cp := &p
	if old := b.providers[p.ID]; old != nil && old.Login == p.Login {
		close(old.ch)
	}
	b.providers[p.ID] = cp
	return cp, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.providers[p.ID] == cp {
			delete(b.providers, p.ID)
			close(cp.ch)
		}
	}
}

// Touch records that the person just typed in the client. Only the login that
// registered the client may touch it.
func (b *Broker) Touch(id, login string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.providers[id]
	if p == nil || p.Login != login {
		return ErrNotAllowed
	}
	p.lastActive = b.now()
	return nil
}

// pick chooses the client to ask for a session: among the clients attached to
// it the one that most recently typed, else among session-less clients the one
// that most recently typed. Nothing else is ever a candidate.
func (b *Broker) pick(session int64) *Provider {
	now := b.now()
	var own, global []*Provider
	for _, p := range b.providers {
		active := p.Always || (!p.lastActive.IsZero() && now.Sub(p.lastActive) <= b.Window)
		if !active {
			continue
		}
		switch p.Session {
		case session:
			own = append(own, p)
		case 0:
			global = append(global, p)
		}
	}
	for _, set := range [][]*Provider{own, global} {
		if len(set) == 0 {
			continue
		}
		sort.Slice(set, func(i, j int) bool {
			if !set[i].lastActive.Equal(set[j].lastActive) {
				return set[i].lastActive.After(set[j].lastActive)
			}
			return set[i].ID < set[j].ID
		})
		return set[0]
	}
	return nil
}

func (b *Broker) allow(session int64) bool {
	now := b.now()
	keep := b.asks[session][:0]
	for _, t := range b.asks[session] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= perSessionPerMinute {
		b.asks[session] = keep
		return false
	}
	b.asks[session] = append(keep, now)
	return true
}

// Ask asks the client currently driving session for its clipboard.
func (b *Broker) Ask(ctx context.Context, session int64, op, typ string) (res Result, err error) {
	start := b.now()
	var prov *Provider
	defer func() {
		if b.Audit == nil {
			return
		}
		f := map[string]any{"session": session, "op": op, "type": typ, "ms": b.now().Sub(start).Milliseconds(), "bytes": len(res.Data)}
		if prov != nil {
			f["client"], f["kind"], f["login"] = prov.ID, prov.Kind, prov.Login
		}
		if err != nil {
			f["outcome"] = err.Error()
		} else {
			f["outcome"] = "ok"
		}
		b.Audit(f)
	}()
	if op != OpList && op != OpRead {
		return Result{}, ErrNotAllowed
	}
	if op == OpRead && !allowedType(typ) {
		return Result{}, ErrNotAllowed
	}
	b.mu.Lock()
	if !b.allow(session) {
		b.mu.Unlock()
		return Result{}, ErrRate
	}
	prov = b.pick(session)
	if prov == nil {
		b.mu.Unlock()
		return Result{}, ErrNoClient
	}
	if !prov.CanRead {
		b.mu.Unlock()
		return Result{}, ErrUnavailable
	}
	b.seq++
	id := prov.ID + "-" + itoa(b.seq)
	pd := &pending{provider: prov.ID, login: prov.Login, done: make(chan Result, 1)}
	b.pending[id] = pd
	select {
	case prov.ch <- Request{ID: id, Op: op, Type: typ}:
	default:
		delete(b.pending, id)
		b.mu.Unlock()
		return Result{}, ErrTimeout
	}
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.pending, id); b.mu.Unlock() }()

	timer := time.NewTimer(b.Timeout)
	defer timer.Stop()
	select {
	case r := <-pd.done:
		if !r.OK {
			return Result{}, ErrUnavailable
		}
		return r, nil
	case <-timer.C:
		return Result{}, ErrTimeout
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// Deliver hands a client's answer to the request waiting for it. Only the
// login and client that were asked may answer.
func (b *Broker) Deliver(reqID, clientID, login string, r Result) error {
	if len(r.Data) > b.MaxBytes {
		r = Result{}
	}
	b.mu.Lock()
	pd := b.pending[reqID]
	b.mu.Unlock()
	if pd == nil || pd.provider != clientID || pd.login != login {
		return ErrNotAllowed
	}
	select {
	case pd.done <- r:
	default:
	}
	return nil
}

// Oversize reports whether n bytes exceeds the cap.
func (b *Broker) Oversize(n int) bool { return n > b.MaxBytes }

func allowedType(t string) bool {
	if t == "text/plain" || t == "text/plain;charset=utf-8" {
		return true
	}
	switch t {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		return true
	}
	return false
}

// AllowedType is exported for the shim and the API.
func AllowedType(t string) bool { return allowedType(strings.ToLower(t)) }

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
