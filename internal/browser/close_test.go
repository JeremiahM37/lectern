package browser

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A click that closes its own window (window.close() in a popup) must come
// back at once. The browser may drop the reply to the mouse event with the
// page, and a call waiting for it used to hang until its deadline: 2 of 20
// clicks with Google Chrome 150.
func TestAClickThatClosesItsWindowReturns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pop" {
			io.WriteString(w, `<title>pop</title><button style="width:100vw;height:100vh" onclick="window.close()">close</button>`)
			return
		}
		io.WriteString(w, `<a href="/pop" target="_blank" style="display:block;width:100vw;height:100vh">open</a>`)
	}))
	defer srv.Close()
	tabs, _ := startTabs(t, LaunchOptions{}, "")
	home, homeID, _ := tabs.Get(0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := home.Navigate(ctx, srv.URL); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := tabs.Select(homeID); err != nil {
			t.Fatal(err)
		}
		if err := home.ClickAt(ctx, 50, 50); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the popup", func() bool { return len(tabs.List()) == 2 })
		pop, popID, _ := tabs.Get(0)
		if popID == homeID {
			t.Fatal("the popup did not become the active tab")
		}
		time.Sleep(100 * time.Millisecond)
		start := time.Now()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := pop.ClickAt(c, 50, 50)
		cancel()
		if err != nil || time.Since(start) > 5*time.Second {
			t.Fatalf("click %d that closes its window: %v after %v", i, err, time.Since(start))
		}
		waitFor(t, "the popup to close", func() bool { return len(tabs.List()) == 1 })
	}
}
