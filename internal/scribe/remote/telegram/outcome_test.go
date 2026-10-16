package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// approvalFor starts an approval through a host of owner and returns the
// decision and the reason it recorded for the agent.
func approvalFor(t *testing.T, h tools.Host) (chan tools.Decision, func() string) {
	t.Helper()
	ctx, reason := tools.WithReasonSlot(context.Background())
	done := make(chan tools.Decision, 1)
	go func() {
		done <- h.Approve(ctx, tools.Approval{Tool: "shell", Kind: "shell", Title: "Run", Detail: "make"})
	}()
	return done, reason
}

func expectDecision(t *testing.T, done chan tools.Decision) tools.Decision {
	t.Helper()
	select {
	case d := <-done:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("approval did not settle")
		return tools.Deny
	}
}

func TestApproveOnPhoneAllowsOnceAndSessionRemembers(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	sink := &fakeSink{}
	s.Bind(sink)

	host := s.HostFor(sink, headlessInner{}) // one host per tab, as the app wires it
	done, _ := approvalFor(t, host)
	m := tg.next(t)
	tg.push(tap(ownerChat, buttonData(t, m, "Approve")))
	if d := expectDecision(t, done); d != tools.AllowOnce {
		t.Fatalf("approve tap = %v, want AllowOnce", d)
	}

	done, _ = approvalFor(t, host)
	m = tg.next(t)
	tg.push(tap(ownerChat, buttonData(t, m, "Approve for session")))
	if d := expectDecision(t, done); d != tools.AllowSession {
		t.Fatalf("session tap = %v, want AllowSession", d)
	}

	// The same kind of action is now remembered for this tab: no phone message.
	done, _ = approvalFor(t, host)
	if d := expectDecision(t, done); d != tools.AllowSession {
		t.Fatalf("remembered approval = %v, want AllowSession without asking", d)
	}
	tg.quiet(t, 100*time.Millisecond)
}

func TestTypedReasonTravelsWithDeny(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	s.Bind(&fakeSink{})

	done, reason := approvalFor(t, s.HostFor(&fakeSink{}, headlessInner{}))
	tg.next(t)
	tg.push(message(ownerChat, "deny too risky, use a dry run"))
	if d := expectDecision(t, done); d != tools.Deny {
		t.Fatalf("decision = %v, want Deny", d)
	}
	if got := reason(); got != "too risky, use a dry run" {
		t.Fatalf("reason = %q", got)
	}
}

func TestPhoneUnreachableDeniesWithReason(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAPI(w, http.StatusInternalServerError, map[string]any{"ok": false, "description": "down"})
	}))
	t.Cleanup(down.Close)
	bot, err := NewBot(Options{Token: testToken, APIBase: down.URL, Allow: staticAllow(t, "4242"), SendGap: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, reason := tools.WithReasonSlot(context.Background())
	// A headless local host: the phone is the only decider, so a failed
	// phone must deny rather than let the local rule allow.
	d := NewHost(bot, headlessInner{}, time.Second).Approve(ctx, tools.Approval{Tool: "web", Kind: "web", Detail: "https://x"})
	if d != tools.Deny {
		t.Fatalf("decision = %v, want Deny when the phone is unreachable", d)
	}
	if !strings.Contains(reason(), "could not be reached") {
		t.Fatalf("reason = %q", reason())
	}
}

func TestClosingTabDeniesItsWaitingApprovals(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	owner := &fakeSink{}
	s.Bind(owner)

	done, reason := approvalFor(t, s.HostFor(owner, headlessInner{}))
	tg.next(t)
	s.CloseTab(owner)
	if d := expectDecision(t, done); d != tools.Deny {
		t.Fatalf("decision = %v, want Deny after the tab closed", d)
	}
	if !strings.Contains(reason(), "tab closed") {
		t.Fatalf("reason = %q", reason())
	}
	if s.IsBound(owner) {
		t.Fatal("a closed tab is still bound to the phone")
	}
}

func TestDisableDeniesWaitingApprovals(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	done, reason := approvalFor(t, s.HostFor(nil, headlessInner{}))
	tg.next(t)
	s.Disable()
	if d := expectDecision(t, done); d != tools.Deny {
		t.Fatalf("decision = %v, want Deny when the surface is turned off", d)
	}
	if !strings.Contains(reason(), "turned off") {
		t.Fatalf("reason = %q", reason())
	}
}

func TestMuteStopsNotesButNotApprovals(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	sink := &fakeSink{}
	s.Bind(sink)

	tg.push(message(ownerChat, "/mute"))
	if m := tg.next(t); !strings.Contains(m.Text, "Muted") {
		t.Fatalf("mute reply = %q", m.Text)
	}
	s.Notify(sink, agent.Event{Kind: agent.EvTurnDone, Text: "all done"})
	tg.quiet(t, 300*time.Millisecond)

	done, _ := approvalFor(t, s.HostFor(sink, headlessInner{}))
	m := tg.next(t)
	if m.Keyboard == nil {
		t.Fatal("an approval sent while muted lost its buttons")
	}
	tg.push(tap(ownerChat, buttonData(t, m, "Deny")))
	expectDecision(t, done)

	tg.push(message(ownerChat, "/unmute"))
	if m := tg.next(t); !strings.Contains(m.Text, "Unmuted") {
		t.Fatalf("unmute reply = %q", m.Text)
	}
	s.Notify(sink, agent.Event{Kind: agent.EvTurnDone, Text: "back"})
	if m := tg.next(t); !strings.Contains(m.Text, "back") {
		t.Fatalf("turn note after unmute = %q", m.Text)
	}
}

func TestNotifyErrorsPostsOnlyErrors(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	sink := &fakeSink{}
	s.Bind(sink)

	tg.push(message(ownerChat, "/notify errors"))
	if m := tg.next(t); !strings.Contains(m.Text, "errors") {
		t.Fatalf("notify reply = %q", m.Text)
	}
	s.Notify(sink, agent.Event{Kind: agent.EvTurnDone, Text: "quiet success"})
	tg.quiet(t, 300*time.Millisecond)

	s.Notify(sink, agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{Error: "provider down"}})
	if m := tg.next(t); !strings.Contains(m.Text, "provider down") {
		t.Fatalf("error note = %q", m.Text)
	}

	tg.push(message(ownerChat, "/notify loudly"))
	if m := tg.next(t); !strings.Contains(m.Text, "Usage: /notify") {
		t.Fatalf("bad notify argument reply = %q", m.Text)
	}
}

func TestSayPassesASlashTextThrough(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	sink := &fakeSink{}
	s.Bind(sink)

	tg.push(message(ownerChat, "/say /clear the cache"))
	tg.next(t)
	if submitted, _ := sink.calls(); len(submitted) != 1 || submitted[0] != "[via telegram] /clear the cache" {
		t.Fatalf("submitted = %q", submitted)
	}
}

func TestCommandWordCapIsEnforced(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	sink := &fakeSink{}
	s.Bind(sink)

	tg.push(message(ownerChat, "/say "+strings.Repeat("w ", maxCommandWords)))
	if m := tg.next(t); !strings.Contains(m.Text, "more than 64 words") {
		t.Fatalf("reply = %q", m.Text)
	}
	if submitted, _ := sink.calls(); len(submitted) != 0 {
		t.Fatal("an over-long command reached the tab")
	}
}

func TestUnpairRemovesAPairedChatButNotAConfigChat(t *testing.T) {
	tg := newFakeTG(t)
	enabledSurface(t, tg).Bind(nil)
	tg.push(message(ownerChat, "/unpair"))
	if m := tg.next(t); !strings.Contains(m.Text, "config") {
		t.Fatalf("static chat unpair reply = %q", m.Text)
	}
}

func TestUnpairRemovesAPairedChat(t *testing.T) {
	tg := newFakeTG(t)
	allow := staticAllow(t)
	if err := allow.Add("777"); err != nil {
		t.Fatal(err)
	}
	startBot(t, tg, allow, nil)
	tg.push(message(777, "/unpair"))
	if m := tg.next(t); !strings.Contains(m.Text, "Unpaired") {
		t.Fatalf("paired chat unpair reply = %q", m.Text)
	}
	if allow.Allowed("777") {
		t.Fatal("the chat is still allowed after /unpair")
	}
}

func TestPhotoIsDownloadedAsAnImagePart(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	sink := &fakeSink{}
	s.Bind(sink)

	u := message(ownerChat, "")
	u.Message.Caption = "what is wrong in this screenshot"
	u.Message.Photo = []PhotoSize{{FileID: "small", FileSize: 4}, {FileID: "big", FileSize: 16}}
	tg.push(u)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(sink.imageParts()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	imgs := sink.imageParts()
	if len(imgs) != 1 || imgs[0].MediaType != "image/jpeg" || len(imgs[0].Data) == 0 {
		t.Fatalf("images = %+v", imgs)
	}
	if submitted, _ := sink.calls(); len(submitted) != 1 || !strings.Contains(submitted[0], "screenshot") {
		t.Fatalf("submitted = %q", submitted)
	}
}

func TestNonImageDocumentIsDescribedNotLoaded(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	sink := &fakeSink{}
	s.Bind(sink)

	u := message(ownerChat, "read this")
	u.Message.Document = &Document{FileID: "doc", FileName: "notes\x07.zip", MimeType: "application/zip", FileSize: 2048}
	tg.push(u)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if submitted, _ := sink.calls(); len(submitted) == 1 {
			if !strings.Contains(submitted[0], "[attachment: notes.zip · application/zip · 2048 bytes — binary not loaded]") {
				t.Fatalf("submitted = %q", submitted[0])
			}
			if len(sink.imageParts()) != 0 {
				t.Fatal("a zip was loaded as an image")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("document message never reached the tab")
}
