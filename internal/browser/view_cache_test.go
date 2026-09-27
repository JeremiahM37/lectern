package browser

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// A dev server that lets its HTML be cached heuristically (Last-Modified far
// in the past, no Cache-Control), as python -m http.server and many static
// servers do. Turning Design Mode on must still reach the page: a browser
// that reuses its cached copy would never get the picker.
func TestDesignModeReachesAPageTheBrowserCached(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	up := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Last-Modified", time.Now().Add(-240*time.Hour).UTC().Format(http.TimeFormat))
		io.WriteString(w, `<html><head><title>Cached app</title></head><body>app</body></html>`)
	})}
	go up.Serve(ln)
	defer up.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	v, err := OpenView(localDial, port, "127.0.0.1", 0, 0, nil, "http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	b := startBrowser(t, Viewport{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	base := "http://127.0.0.1:" + strconv.Itoa(v.ListenPort)
	if _, err := b.Navigate(ctx, base+"/?"+TicketParam+"="+v.Ticket()); err != nil {
		t.Fatal(err)
	}
	if n, _ := b.Evaluate(ctx, "document.querySelectorAll('script[src=\"/__lectern/design.js\"]').length"); n != float64(0) {
		t.Fatalf("picker before Design Mode: %v", n)
	}
	v.SetDesign(true)
	if _, err := b.Navigate(ctx, base+"/?"+TicketParam+"="+v.Ticket()); err != nil {
		t.Fatal(err)
	}
	if n, _ := b.Evaluate(ctx, "document.querySelectorAll('script[src=\"/__lectern/design.js\"]').length"); n != float64(1) {
		t.Fatalf("Design Mode did not reach a page the browser had cached (%d requests reached the dev server)", hits)
	}
}
