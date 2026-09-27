package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// An agent on the operator's machine reaches the API as a local process. It
// must not be able to take the operator's side of the browser: turn on
// computer use, hand itself control back, open a view, or speak for the
// operator through Design Mode.
func TestAgentsCannotTakeTheOperatorsSideOfTheBrowser(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "t", Kind: "local", MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := db.InsertProject(&store.Project{Name: "p", TargetID: target.ID, RepoPath: "/tmp/p"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: "s", Agent: "claude", Workdir: "/tmp/p",
		TmuxSession: "s", Status: "idle", Origin: "adopted"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{DB: db, Auth: &auth.Resolver{Mode: auth.ModeTailscale}, Cfg: nil}
	design := `{"source":"browser","elements":[{"selector":"#a"}]}`
	for _, tc := range []struct {
		name, method, id, body string
		handler                http.HandlerFunc
	}{
		{"enable computer use", "PATCH", itoa(proj.ID), `{"computer_use":true}`, s.patchProject},
		{"hand control to the agent", "POST", itoa(sess.ID), `{"action":"control","mode":"agent"}`, s.postSessionBrowser},
		{"drive the pane's browser", "POST", itoa(sess.ID), `{"action":"navigate","url":"http://localhost:1"}`, s.postSessionBrowser},
		{"open a view", "POST", itoa(sess.ID), `{"port":3000,"parent_origin":"http://x:1"}`, s.openBrowserView},
		{"send a design selection", "POST", itoa(sess.ID), design, s.sendDesign},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/", strings.NewReader(tc.body))
			r.SetPathValue("id", tc.id)
			r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindLocal}))
			w := httptest.NewRecorder()
			tc.handler(w, r)
			if w.Code != 403 {
				t.Fatalf("a local process was allowed: %d %s", w.Code, w.Body.String())
			}
		})
	}
	if p, _ := db.Project(proj.ID); p.ComputerUse != 0 {
		t.Fatal("computer use was turned on")
	}
	// Turning it off only takes power away, so anyone may.
	r := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"computer_use":false}`))
	r.SetPathValue("id", itoa(proj.ID))
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindLocal}))
	w := httptest.NewRecorder()
	s.patchProject(w, r)
	if w.Code != 200 {
		t.Fatalf("turning computer use off: %d %s", w.Code, w.Body.String())
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
