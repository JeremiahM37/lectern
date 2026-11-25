package api_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/version"
)

type fakeUpdater struct {
	latest    string
	blocked   string
	installed []string
	fail      error
}

func (f *fakeUpdater) Latest(context.Context) (string, error) { return f.latest, nil }
func (f *fakeUpdater) Blocked() string                        { return f.blocked }
func (f *fakeUpdater) Install(_ context.Context, tag string) error {
	if f.fail != nil {
		return f.fail
	}
	f.installed = append(f.installed, tag)
	return nil
}

// The UI's "Update and restart all": GET says a newer release exists, POST
// installs it, answers, and only then restarts the server, asking the new
// one to restart every session.
func TestSelfUpdateInstallsThenRestarts(t *testing.T) {
	h := newHarness(t)
	old := version.Version
	version.Version = "2.10.5"
	t.Cleanup(func() { version.Version = old })
	fake := &fakeUpdater{latest: "v2.10.6"}
	restarted := make(chan bool, 1)
	h.App.Server.Updater = fake
	h.App.Server.Restart = func(sessions bool) { restarted <- sessions }

	var view obj
	h.decode("GET", "/api/update?refresh=1", nil, 200, &view)
	if view["available"] != true || view["can_update"] != true || view.str("latest") != "v2.10.6" || view.str("current") != "2.10.5" {
		t.Fatalf("update view = %v", view)
	}
	var res obj
	h.decode("POST", "/api/update", obj{}, 202, &res)
	if res.str("to") != "v2.10.6" || len(fake.installed) != 1 {
		t.Fatalf("post = %v, installed %v", res, fake.installed)
	}
	select {
	case sessions := <-restarted:
		if !sessions {
			t.Fatal("sessions should restart after an update by default")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not restart after installing")
	}
	h.decode("GET", "/api/update", nil, 200, &view)
	if view.str("phase") != "restarting" || view.str("target") != "v2.10.6" {
		t.Fatalf("after install: %v", view)
	}
	// A second click while it restarts is refused.
	h.decode("POST", "/api/update", obj{}, 409, nil)
}

func TestSelfUpdateRefusals(t *testing.T) {
	h := newHarness(t)
	old := version.Version
	version.Version = "2.10.6"
	t.Cleanup(func() { version.Version = old })
	fake := &fakeUpdater{latest: "v2.10.6"}
	h.App.Server.Updater = fake
	h.App.Server.Restart = func(bool) { t.Error("must not restart") }

	var view obj
	h.decode("GET", "/api/update?refresh=1", nil, 200, &view)
	if view["available"] != false {
		t.Fatalf("already latest: %v", view)
	}
	h.decode("POST", "/api/update", obj{}, 409, nil)

	version.Version = "2.10.5"
	fake.blocked = "the server cannot replace /usr/local/bin/lectern"
	h.decode("GET", "/api/update", nil, 200, &view)
	if view["can_update"] != false || view.str("reason") == "" {
		t.Fatalf("blocked: %v", view)
	}
	h.decode("POST", "/api/update", obj{}, 409, nil)

	fake.blocked, fake.fail = "", errors.New("checksum mismatch for lectern_linux_amd64.tar.gz; nothing was changed")
	h.decode("POST", "/api/update", obj{}, 502, nil)
	h.decode("GET", "/api/update", nil, 200, &view)
	if view.str("phase") != "failed" || view.str("error") == "" {
		t.Fatalf("failed install: %v", view)
	}
	if len(fake.installed) != 0 {
		t.Fatal("nothing should have been installed")
	}
}

func TestSelfUpdateUnavailableWithoutUpdater(t *testing.T) {
	h := newHarness(t)
	var view obj
	h.decode("GET", "/api/update", nil, 200, &view)
	if view["can_update"] != false || view.str("reason") == "" {
		t.Fatalf("no updater: %v", view)
	}
	h.decode("POST", "/api/update", obj{}, 501, nil)
}
