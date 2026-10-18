package main

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// w30sigLog records shutdown calls across every tab, in order.
type w30sigLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *w30sigLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, s)
}

func (l *w30sigLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// w30sigTab is a tab that records Abort and Close. panicOnClose makes its
// close fail, so the test can see the failure come back from endSessions.
type w30sigTab struct {
	*tui.FakeBackend
	name         string
	log          *w30sigLog
	panicOnClose bool
}

func (t *w30sigTab) Abort() { t.log.add("abort:" + t.name) }

func (t *w30sigTab) Close() error {
	t.log.add("close:" + t.name)
	if t.panicOnClose {
		panic("save failed")
	}
	return nil
}

// Every tab's turn is aborted before any tab is closed, so no tab's close runs
// while another tab's turn is still going.
func TestEndSessionsAbortsEveryTabBeforeClosingW30sig(t *testing.T) {
	log := &w30sigLog{}
	a := &w30sigTab{FakeBackend: tui.NewFakeBackend(), name: "a", log: log}
	b := &w30sigTab{FakeBackend: tui.NewFakeBackend(), name: "b", log: log}

	errs := endSessions([]tui.Backend{a, b})
	if len(errs) != 0 {
		t.Fatalf("errors = %v, want none", errs)
	}
	want := "abort:a abort:b close:a close:b"
	if got := strings.Join(log.snapshot(), " "); got != want {
		t.Fatalf("call order:\n got  %s\n want %s", got, want)
	}
}

// A failing step must come back to the caller, and the other tabs still close.
func TestEndSessionsReturnsAFailedCloseAndKeepsGoingW30sig(t *testing.T) {
	log := &w30sigLog{}
	bad := &w30sigTab{FakeBackend: tui.NewFakeBackend(), name: "bad", log: log, panicOnClose: true}
	good := &w30sigTab{FakeBackend: tui.NewFakeBackend(), name: "good", log: log}

	errs := endSessions([]tui.Backend{bad, good})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "close session") {
		t.Fatalf("errors = %v, want one close-session failure", errs)
	}
	if got := log.snapshot(); !contains(got, "close:good") {
		t.Fatalf("the tab after the failure was not closed: %v", got)
	}
}

// Failures print once each, even when the quit screen recorded the same one.
func TestPrintShutdownErrorsPrintsEachOnceW30sig(t *testing.T) {
	var buf bytes.Buffer
	dup := errors.New("close session: no answer after 10s, abandoned")
	printShutdownErrors(&buf, []error{dup, dup, errors.New("kill process groups: stuck")})

	out := buf.String()
	if n := strings.Count(out, "no answer after 10s"); n != 1 {
		t.Fatalf("repeated failure printed %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "kill process groups: stuck") {
		t.Fatalf("second failure missing:\n%s", out)
	}
}

func TestPrintShutdownErrorsSilentWhenCleanW30sig(t *testing.T) {
	var buf bytes.Buffer
	printShutdownErrors(&buf, nil)
	if buf.Len() != 0 {
		t.Fatalf("a clean shutdown printed %q", buf.String())
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
