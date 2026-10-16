package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestFindBinaryPrefersPATH(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, binaryName)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	vendored := filepath.Join(t.TempDir(), "vendored")
	if err := os.WriteFile(vendored, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got, err := FindBinary(vendored)
	if err != nil || got != bin {
		t.Fatalf("FindBinary = %q, %v; want the PATH binary %q", got, err, bin)
	}
}

func TestFindBinaryFallsBackToVendoredPin(t *testing.T) {
	vendored := filepath.Join(t.TempDir(), "vendored")
	if err := os.WriteFile(vendored, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if got, err := FindBinary(vendored); err != nil || got != vendored {
		t.Fatalf("FindBinary = %q, %v; want the vendored pin", got, err)
	}
}

func TestFindBinaryRejectsNonExecutableAndMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	notExec := filepath.Join(t.TempDir(), "pin")
	if err := os.WriteFile(notExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"", notExec, filepath.Join(t.TempDir(), "absent")} {
		if _, err := FindBinary(v); err != ErrNotInstalled {
			t.Fatalf("FindBinary(%q) err = %v, want ErrNotInstalled", v, err)
		}
	}
	if ErrNotInstalled.Error() != "Proxy addon not installed" {
		t.Fatalf("message = %q", ErrNotInstalled.Error())
	}
}

func TestWaitHealthyRetriesUntilUp(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	st := waitHealthy(context.Background(), srv.Client(), srv.URL, "k", 10, time.Millisecond)
	if st != Running || calls.Load() != 3 {
		t.Fatalf("state %q after %d probes, want running after 3", st, calls.Load())
	}
}

func TestWaitHealthyGivesUpAfterAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if st := waitHealthy(context.Background(), srv.Client(), srv.URL, "", 4, time.Millisecond); st != Unreachable || calls.Load() != 4 {
		t.Fatalf("state %q after %d probes, want unreachable after 4", st, calls.Load())
	}
}

func TestWaitHealthyStopsWhenCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if st := waitHealthy(ctx, srv.Client(), srv.URL, "", 10, time.Hour); st != Unreachable {
		t.Fatalf("state %q", st)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancelled wait did not return promptly")
	}
}

func TestWaitHealthyNeedsAuthIsUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if st := waitHealthy(context.Background(), srv.Client(), srv.URL, "", 10, time.Hour); st != NeedsAuth {
		t.Fatalf("state %q, want needs-auth at once", st)
	}
}
