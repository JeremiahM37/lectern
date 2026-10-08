package clipboard

import (
	"context"
	"errors"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestBroker() (*Broker, *clock) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	b := NewBroker()
	b.Now = c.now
	b.Timeout = 300 * time.Millisecond
	return b, c
}

// answer serves one provider's requests with a fixed image.
func answer(b *Broker, p *Provider, login string, data []byte) {
	go func() {
		for req := range p.Requests() {
			b.Deliver(req.ID, p.ID, login, Result{OK: true, Type: req.Type, Types: []string{"image/png"}, Data: data})
		}
	}()
}

func TestAskRoutesToTheTypingClientOfThatSession(t *testing.T) {
	b, c := newTestBroker()
	laptop, _ := b.Register(Provider{ID: "laptop", Session: 7, Login: "u", CanRead: true})
	phone, _ := b.Register(Provider{ID: "phone", Session: 7, Login: "u", CanRead: true})
	answer(b, laptop, "u", []byte("LAPTOP"))
	answer(b, phone, "u", []byte("PHONE"))
	b.Touch("laptop", "u")
	c.t = c.t.Add(time.Second)
	b.Touch("phone", "u")
	res, err := b.Ask(context.Background(), 7, OpRead, "image/png")
	if err != nil || string(res.Data) != "PHONE" {
		t.Fatalf("most recent typist: %v %q", err, res.Data)
	}
	c.t = c.t.Add(time.Second)
	b.Touch("laptop", "u")
	res, _ = b.Ask(context.Background(), 7, OpRead, "image/png")
	if string(res.Data) != "LAPTOP" {
		t.Fatalf("got %q", res.Data)
	}
}

func TestAskRefusesClientsThatAreIdle(t *testing.T) {
	b, c := newTestBroker()
	p, _ := b.Register(Provider{ID: "laptop", Session: 7, Login: "u", CanRead: true})
	answer(b, p, "u", []byte("X"))
	if _, err := b.Ask(context.Background(), 7, OpRead, "image/png"); !errors.Is(err, ErrNoClient) {
		t.Fatalf("never typed: %v", err)
	}
	b.Touch("laptop", "u")
	c.t = c.t.Add(b.Window + time.Second)
	if _, err := b.Ask(context.Background(), 7, OpRead, "image/png"); !errors.Is(err, ErrNoClient) {
		t.Fatalf("idle: %v", err)
	}
}

func TestAnotherSessionsClientIsNeverAsked(t *testing.T) {
	b, _ := newTestBroker()
	other, _ := b.Register(Provider{ID: "other", Session: 8, Login: "u", CanRead: true})
	answer(b, other, "u", []byte("SECRET"))
	b.Touch("other", "u")
	if res, err := b.Ask(context.Background(), 7, OpRead, "image/png"); err == nil {
		t.Fatalf("session 7 read session 8's client: %q", res.Data)
	}
}

func TestSessionlessClientIsTheFallback(t *testing.T) {
	b, _ := newTestBroker()
	app, _ := b.Register(Provider{ID: "app", Login: "u", CanRead: true})
	answer(b, app, "u", []byte("APP"))
	b.Touch("app", "u")
	res, err := b.Ask(context.Background(), 7, OpRead, "image/png")
	if err != nil || string(res.Data) != "APP" {
		t.Fatalf("%v %q", err, res.Data)
	}
	// an attached client wins over the fallback even if it typed earlier
	own, _ := b.Register(Provider{ID: "own", Session: 7, Login: "u", CanRead: true})
	answer(b, own, "u", []byte("OWN"))
	b.Touch("own", "u")
	res, _ = b.Ask(context.Background(), 7, OpRead, "image/png")
	if string(res.Data) != "OWN" {
		t.Fatalf("got %q", res.Data)
	}
}

func TestOnlyTheRegisteringLoginCanTouchOrAnswer(t *testing.T) {
	b, _ := newTestBroker()
	p, _ := b.Register(Provider{ID: "laptop", Session: 7, Login: "owner", CanRead: true, Always: true})
	if err := b.Touch("laptop", "mallory"); err == nil {
		t.Fatal("another login touched the client")
	}
	done := make(chan error, 1)
	go func() {
		req := <-p.Requests()
		// wrong client id, then wrong login: both refused; the right one works
		e1 := b.Deliver(req.ID, "someone-else", "owner", Result{OK: true, Data: []byte("x")})
		e2 := b.Deliver(req.ID, "laptop", "mallory", Result{OK: true, Data: []byte("x")})
		if e1 == nil || e2 == nil {
			done <- errors.New("forged answer accepted")
			return
		}
		done <- b.Deliver(req.ID, "laptop", "owner", Result{OK: true, Data: []byte("real")})
	}()
	res, err := b.Ask(context.Background(), 7, OpRead, "image/png")
	if err != nil || string(res.Data) != "real" {
		t.Fatalf("%v %q", err, res.Data)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestAskTimesOutAndCapsSize(t *testing.T) {
	b, _ := newTestBroker()
	b.MaxBytes = 4
	p, _ := b.Register(Provider{ID: "slow", Session: 7, Login: "u", CanRead: true, Always: true})
	start := time.Now()
	if _, err := b.Ask(context.Background(), 7, OpRead, "image/png"); !errors.Is(err, ErrTimeout) {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("did not time out promptly")
	}
	<-p.Requests() // drain the timed-out request
	answer(b, p, "u", []byte("0123456789"))
	if _, err := b.Ask(context.Background(), 7, OpRead, "image/png"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("oversize answer: %v", err)
	}
}

func TestBrowserClientAnswersUnavailableAtOnce(t *testing.T) {
	b, _ := newTestBroker()
	b.Timeout = 5 * time.Second
	b.Register(Provider{ID: "web", Session: 7, Login: "u", CanRead: false, Kind: "web"})
	b.Touch("web", "u")
	start := time.Now()
	if _, err := b.Ask(context.Background(), 7, OpList, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("a browser must answer at once")
	}
}

func TestRefusesUnknownOpsTypesAndFloods(t *testing.T) {
	b, _ := newTestBroker()
	p, _ := b.Register(Provider{ID: "c", Session: 7, Login: "u", CanRead: true, Always: true})
	answer(b, p, "u", []byte("x"))
	for _, c := range []struct{ op, typ string }{{"write", "image/png"}, {OpRead, "application/x-secret"}, {OpRead, "text/html"}} {
		if _, err := b.Ask(context.Background(), 7, c.op, c.typ); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%v: %v", c, err)
		}
	}
	var last error
	for i := 0; i < perSessionPerMinute+5; i++ {
		_, last = b.Ask(context.Background(), 7, OpRead, "image/png")
	}
	if !errors.Is(last, ErrRate) {
		t.Fatalf("flood: %v", last)
	}
}

func TestAuditLine(t *testing.T) {
	b, _ := newTestBroker()
	var got map[string]any
	b.Audit = func(f map[string]any) { got = f }
	p, _ := b.Register(Provider{ID: "c", Session: 7, Login: "u", Kind: "cli", CanRead: true, Always: true})
	answer(b, p, "u", []byte("abc"))
	b.Ask(context.Background(), 7, OpRead, "image/png")
	if got["session"] != int64(7) || got["client"] != "c" || got["outcome"] != "ok" || got["bytes"] != 3 || got["login"] != "u" {
		t.Fatalf("%v", got)
	}
}
