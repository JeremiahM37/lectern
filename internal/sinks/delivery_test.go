package sinks

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/internal/push"
	"github.com/JeremiahM37/lectern/internal/store"
)

// BuildPayloads was tested; actually delivering them was not. A notification
// that is built correctly and never sent is the same as no notification, and it
// fails silently by design — Notify cannot return an error to anyone.

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/n.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// recorder is a sink that remembers what arrived.
type recorder struct {
	mu   sync.Mutex
	hits []map[string]any
	code int
}

func (r *recorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		json.NewDecoder(req.Body).Decode(&body)
		r.mu.Lock()
		r.hits = append(r.hits, body)
		r.mu.Unlock()
		if r.code != 0 {
			w.WriteHeader(r.code)
			return
		}
		w.WriteHeader(200)
	}
}

func (r *recorder) got() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.hits...)
}

func TestNotifyDeliversToEveryConfiguredSink(t *testing.T) {
	db := testDB(t)
	discord, ntfy := &recorder{}, &recorder{}
	ds := httptest.NewServer(discord.handler())
	ns := httptest.NewServer(ntfy.handler())
	defer ds.Close()
	defer ns.Close()

	db.SetSetting("discord_webhook", ds.URL)
	db.SetSetting("ntfy_server", ns.URL)
	db.SetSetting("ntfy_topic", "homelab")

	n := &Notifier{DB: db, BaseURL: "http://deck:9110", Log: slog.Default(), Client: ds.Client()}
	n.Notify("Task failed", "build broke on lxc-104", "/tasks/7", nil)
	n.Wait()

	if len(discord.got()) != 1 {
		t.Fatalf("discord received %d notifications", len(discord.got()))
	}
	if content, _ := discord.got()[0]["content"].(string); content == "" {
		t.Errorf("discord payload has no content: %v", discord.got()[0])
	}
	hits := ntfy.got()
	if len(hits) != 1 {
		t.Fatalf("ntfy received %d notifications", len(hits))
	}
	if hits[0]["topic"] != "homelab" {
		t.Errorf("ntfy topic: %v", hits[0]["topic"])
	}
	// the click-through has to be an address the phone can actually open
	if click, _ := hits[0]["click"].(string); click != "http://deck:9110/tasks/7" {
		t.Errorf("ntfy click url: %q", click)
	}
}

// One dead sink must not swallow the others — that is the whole point of
// best-effort fan-out, and it is exactly what a silent failure would hide.
func TestOneFailingSinkDoesNotStopTheOthers(t *testing.T) {
	db := testDB(t)
	good := &recorder{}
	gs := httptest.NewServer(good.handler())
	defer gs.Close()

	db.SetSetting("discord_webhook", "http://127.0.0.1:1/dead") // refuses instantly
	db.SetSetting("ntfy_server", gs.URL)
	db.SetSetting("ntfy_topic", "homelab")

	n := &Notifier{DB: db, BaseURL: "http://deck", Log: slog.Default()}
	n.Notify("still delivered", "body", "/", nil)
	n.Wait()

	if len(good.got()) != 1 {
		t.Errorf("the healthy sink got %d notifications after the dead one failed", len(good.got()))
	}
}

// A sink answering 500 must not hang the shutdown path or panic.
func TestASinkErrorIsSurvived(t *testing.T) {
	db := testDB(t)
	bad := &recorder{code: 500}
	bs := httptest.NewServer(bad.handler())
	defer bs.Close()
	db.SetSetting("discord_webhook", bs.URL)

	n := &Notifier{DB: db, BaseURL: "http://deck", Log: slog.Default()}
	n.Notify("t", "b", "/", nil)
	done := make(chan struct{})
	go func() { n.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Wait never returned after a sink error")
	}
	if len(bad.got()) != 1 {
		t.Errorf("the request was never made")
	}
}

// Notify must return immediately: it is called from the scheduler tick, and a
// slow webhook stalling that would stall every running attempt.
func TestNotifyDoesNotBlockTheCaller(t *testing.T) {
	db := testDB(t)
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(200)
	}))
	defer slow.Close()
	defer close(release)
	db.SetSetting("discord_webhook", slow.URL)

	n := &Notifier{DB: db, BaseURL: "http://deck", Log: slog.Default()}
	start := time.Now()
	n.Notify("t", "b", "/", nil)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Notify blocked the caller for %v", elapsed)
	}
}

// Nothing configured means nothing sent — and no goroutine left behind.
func TestNoSinksMeansNoDelivery(t *testing.T) {
	db := testDB(t)
	n := &Notifier{DB: db, BaseURL: "http://deck", Log: slog.Default()}
	n.Notify("t", "b", "/", nil)
	n.Wait() // must not hang
}

// ---- web push ----------------------------------------------------------

// A browser that has revoked its subscription answers 410. Keeping that row
// means every later notification wastes a request on a dead endpoint forever.
func TestADeadPushSubscriptionIsPruned(t *testing.T) {
	db := testDB(t)
	var seen int
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		seen++
		w.WriteHeader(410) // Gone
	}))
	defer dead.Close()

	priv, _ := vapidTestKey(t)
	sender := &push.Sender{PrivateKey: priv, PublicKey: "pub", Email: "a@b", Log: slog.Default()}
	if err := Subscribe(db, push.Subscription{
		Endpoint: dead.URL, Keys: browserTestKeys(t)}); err != nil {
		t.Fatal(err)
	}

	n := &Notifier{DB: db, BaseURL: "http://deck", Push: sender, Log: slog.Default()}
	n.Notify("approval", "Bash: rm -rf build/", "/approvals", &Extra{Kind: "approval", ApprovalID: 4})
	n.Wait()

	if seen == 0 {
		t.Fatal("the push was never attempted")
	}
	count, err := db.Count("push_subscriptions", "1=1")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("a subscription the browser reported Gone is still stored (%d rows)", count)
	}
}

// A healthy subscription must survive: pruning too eagerly silently turns push
// off for the one device the operator actually carries.
func TestAHealthyPushSubscriptionIsKept(t *testing.T) {
	db := testDB(t)
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(201)
	}))
	defer ok.Close()

	priv, _ := vapidTestKey(t)
	n := &Notifier{DB: db, BaseURL: "http://deck", Log: slog.Default(),
		Push: &push.Sender{PrivateKey: priv, PublicKey: "pub", Email: "a@b", Log: slog.Default()}}
	Subscribe(db, push.Subscription{Endpoint: ok.URL, Keys: browserTestKeys(t)})

	n.Notify("t", "b", "/", nil)
	n.Wait()
	if count, _ := db.Count("push_subscriptions", "1=1"); count != 1 {
		t.Errorf("a healthy subscription was dropped (%d rows)", count)
	}
}

// Re-subscribing from the same browser must refresh the row, not accumulate
// duplicates that each get their own copy of every notification.
func TestResubscribingReplacesTheSameEndpoint(t *testing.T) {
	db := testDB(t)
	sub := push.Subscription{Endpoint: "https://push.example/abc",
		Keys: map[string]string{"p256dh": "one", "auth": "x"}}
	if err := Subscribe(db, sub); err != nil {
		t.Fatal(err)
	}
	sub.Keys = map[string]string{"p256dh": "two", "auth": "y"}
	if err := Subscribe(db, sub); err != nil {
		t.Fatal(err)
	}
	if count, _ := db.Count("push_subscriptions", "1=1"); count != 1 {
		t.Fatalf("re-subscribing created a duplicate row (%d)", count)
	}
	var keys string
	db.QueryRow(`SELECT keys_json FROM push_subscriptions`).Scan(&keys)
	if !strings.Contains(keys, "two") {
		t.Errorf("the refreshed keys were not stored: %s", keys)
	}
}

// ---- test keys ---------------------------------------------------------

func vapidTestKey(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	d := make([]byte, 32)
	key.D.FillBytes(d)
	return base64.RawURLEncoding.EncodeToString(d),
		base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), key.X, key.Y))
}

func browserTestKeys(t *testing.T) map[string]string {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return map[string]string{
		"p256dh": base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		"auth":   base64.RawURLEncoding.EncodeToString(auth),
	}
}
