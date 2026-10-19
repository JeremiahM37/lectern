package codex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStartAppServerHandshakeAndRequest(t *testing.T) {
	f := newFakeCodex(t, "ok", "", true)
	c, err := StartAppServer(context.Background(), f.bin)
	if err != nil {
		t.Fatalf("StartAppServer: %v", err)
	}
	var resp struct {
		Account struct {
			Email string `json:"email"`
		} `json:"account"`
	}
	if err := c.Request(context.Background(), "account/read", map[string]any{}, &resp); err != nil {
		t.Fatalf("account/read: %v", err)
	}
	if resp.Account.Email != "tester@example.com" {
		t.Fatalf("email = %q", resp.Account.Email)
	}
	pid := f.pid(t)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !processGone(pid) {
		t.Fatalf("child %d still exists right after Close", pid)
	}
	if !f.hasRequest(t, "initialize") {
		t.Fatalf("no initialize was sent; requests = %v", f.requests(t))
	}
}

func TestRequestJSONRPCErrorCarriesServerMessage(t *testing.T) {
	f := newFakeCodex(t, "rpc-error", "", false)
	c, err := StartAppServer(context.Background(), f.bin)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Request(context.Background(), "account/read", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "account/read") {
		t.Fatalf("err = %v, want the method and the server message", err)
	}
}

func TestNotificationsInterleavedAreSkippedAndEarlyOnesKept(t *testing.T) {
	f := newFakeCodex(t, "device-early", "success", false)
	c, err := StartAppServer(context.Background(), f.bin)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The fake sends the completion before the reply to login/start. The
	// notification must still be there when WaitNotification asks for it.
	var start struct {
		LoginID string `json:"loginId"`
	}
	if err := c.Request(ctx, "account/login/start", map[string]string{"type": "chatgptDeviceCode"}, &start); err != nil {
		t.Fatalf("login/start: %v", err)
	}
	params, err := c.WaitNotification(ctx, "account/login/completed", func(p json.RawMessage) bool {
		return loginIDOf(p) == start.LoginID
	})
	if err != nil {
		t.Fatalf("WaitNotification: %v", err)
	}
	if !strings.Contains(string(params), `"success":true`) {
		t.Fatalf("params = %s", params)
	}
}

func TestUnknownNotificationsDoNotDisturbRequests(t *testing.T) {
	f := newFakeCodex(t, "interleave", "", true)
	c, err := StartAppServer(context.Background(), f.bin)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var resp map[string]any
	if err := c.Request(context.Background(), "account/read", map[string]any{}, &resp); err != nil {
		t.Fatalf("account/read with interleaved notifications: %v", err)
	}
	if _, ok := resp["account"]; !ok {
		t.Fatalf("reply = %v", resp)
	}
}

func TestWaitNotificationStopsWithContext(t *testing.T) {
	f := newFakeCodex(t, "device-hang", "", false)
	c, err := StartAppServer(context.Background(), f.bin)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = c.WaitNotification(ctx, "account/login/completed", nil)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("err = %v, want the context's error", err)
	}
}

func TestWaitNotificationFailsWhenServerExits(t *testing.T) {
	f := newFakeCodex(t, "ok", "", true)
	c, err := StartAppServer(context.Background(), f.bin)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Closing the client makes the server exit. A waiter that is still
	// running must then return an error instead of hanging.
	done := make(chan error, 1)
	go func() {
		_, err := c.WaitNotification(context.Background(), "account/login/completed", nil)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	_ = c.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("WaitNotification returned nil after the server exited")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitNotification did not return after the server exited")
	}
}

func TestCancelledContextStopsChildDuringHandshake(t *testing.T) {
	f := newFakeCodex(t, "hang-init", "", false)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c, err := StartAppServer(ctx, f.bin)
	if err == nil {
		c.Close()
		t.Fatal("StartAppServer succeeded against a server that never answers")
	}
	if !processGone(f.pid(t)) {
		t.Fatal("the child is still running after StartAppServer failed")
	}
}

func TestCancelledContextStopsRunningChild(t *testing.T) {
	f := newFakeCodex(t, "ok", "", true)
	ctx, cancel := context.WithCancel(context.Background())
	c, err := StartAppServer(ctx, f.bin)
	if err != nil {
		t.Fatal(err)
	}
	pid := f.pid(t)
	cancel()
	waitGone(t, pid, 5*time.Second)
	if err := c.Request(context.Background(), "account/read", map[string]any{}, nil); err == nil {
		t.Fatal("Request succeeded on a client whose context was cancelled")
	}
	_ = c.Close()
}

func TestCloseEscalatesToKillWhenTermIsIgnored(t *testing.T) {
	f := newFakeCodex(t, "ignore-term", "", true)
	c, err := StartAppServer(context.Background(), f.bin)
	if err != nil {
		t.Fatal(err)
	}
	pid := f.pid(t)
	start := time.Now()
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < closeGrace/2 {
		t.Fatalf("Close returned after %v; it should have waited for SIGTERM to be ignored", elapsed)
	}
	if elapsed > closeGrace+5*time.Second {
		t.Fatalf("Close took %v", elapsed)
	}
	if !processGone(pid) {
		t.Fatalf("child %d survived Close", pid)
	}
}

func TestCloseIsIdempotentAndRequestAfterCloseFails(t *testing.T) {
	f := newFakeCodex(t, "ok", "", true)
	c, err := StartAppServer(context.Background(), f.bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := c.Request(context.Background(), "account/read", map[string]any{}, nil); err == nil {
		t.Fatal("Request after Close succeeded")
	}
}

func TestStartAppServerMissingBinary(t *testing.T) {
	if _, err := StartAppServer(context.Background(), "/nonexistent/codex-binary"); err == nil {
		t.Fatal("StartAppServer succeeded for a missing binary")
	}
}
