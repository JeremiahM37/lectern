package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fixture builds a home with a live WAL database, media, ~/.lectern and
// transcripts for one Claude and one Codex workspace, plus some that belong
// to no session.
func fixture(t *testing.T) Source {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dbPath := filepath.Join(home, "state", "lectern.db")
	os.MkdirAll(filepath.Dir(dbPath), 0o700)
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		"CREATE TABLE sessions(id INTEGER PRIMARY KEY, name TEXT)",
		"INSERT INTO sessions(name) VALUES ('one'), ('two')",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	work := filepath.Join(root, "work", "app")
	other := filepath.Join(root, "work", "other")
	slug := func(p string) string {
		return strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				return r
			}
			return '-'
		}, p)
	}
	write(t, filepath.Join(home, ".claude", "projects", slug(work), "c1.jsonl"), `{"type":"user"}`+"\n")
	write(t, filepath.Join(home, ".claude", "projects", slug(other), "x.jsonl"), "not ours\n")
	write(t, filepath.Join(home, ".codex", "sessions", "2026", "11", "09", "rollout-a.jsonl"),
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":"a","cwd":%q}}`+"\n", work))
	write(t, filepath.Join(home, ".codex", "sessions", "2026", "11", "09", "rollout-b.jsonl"),
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":"b","cwd":%q}}`+"\n", other))
	write(t, filepath.Join(home, ".lectern", "plugins", "p", "plugin.json"), `{"id":"p"}`)
	media := filepath.Join(root, "media")
	write(t, filepath.Join(media, "clip.mp4"), "video")
	return Source{
		DBPath: dbPath, MediaDir: media, Home: home, Version: "test", Host: "box",
		Workspaces: []Workspace{{Agent: "claude", Dir: work, Target: "local"}, {Agent: "codex", Dir: work, Target: "local"}},
		Env:        []string{"LECTERN_TLS_PORT=8443"},
	}
}

func TestBackupRestoresEverythingOntoAFreshMachine(t *testing.T) {
	src := fixture(t)
	var buf bytes.Buffer
	m, err := Write(context.Background(), &buf, src, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if m.Sessions != 2 || len(m.Skipped) != 0 {
		t.Fatalf("manifest = %+v", m)
	}
	if bytes.Contains(buf.Bytes(), []byte("not ours")) || !bytes.HasPrefix(buf.Bytes(), []byte("age-encryption.org/")) {
		t.Fatal("archive is not encrypted")
	}

	fresh := t.TempDir()
	home := filepath.Join(fresh, "newhome")
	to := Target{DBPath: filepath.Join(home, "state", "lectern.db"), Home: home, MediaDir: filepath.Join(fresh, "media2")}
	res, err := Restore(bytes.NewReader(buf.Bytes()), "correct horse", to)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Database || res.Kept != 0 {
		t.Fatalf("result = %+v", res)
	}
	db, err := sql.Open("sqlite", "file:"+to.DBPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sessions").Scan(&n); err != nil || n != 2 {
		t.Fatalf("restored sessions = %d, %v", n, err)
	}
	if got := read(t, filepath.Join(home, ".lectern", "plugins", "p", "plugin.json")); got != `{"id":"p"}` {
		t.Fatalf("plugin = %q", got)
	}
	if got := read(t, filepath.Join(to.MediaDir, "clip.mp4")); got != "video" {
		t.Fatalf("media = %q", got)
	}
	if got := read(t, res.EnvFile); got != "LECTERN_TLS_PORT=8443\n" {
		t.Fatalf("env = %q", got)
	}
	var claude, codex []string
	filepath.Walk(filepath.Join(home, ".claude"), func(p string, _ os.FileInfo, _ error) error {
		if strings.HasSuffix(p, ".jsonl") {
			claude = append(claude, filepath.Base(p))
		}
		return nil
	})
	filepath.Walk(filepath.Join(home, ".codex"), func(p string, _ os.FileInfo, _ error) error {
		if strings.HasSuffix(p, ".jsonl") {
			codex = append(codex, filepath.Base(p))
		}
		return nil
	})
	if strings.Join(claude, ",") != "c1.jsonl" || strings.Join(codex, ",") != "rollout-a.jsonl" {
		t.Fatalf("transcripts: claude %v codex %v (only the sessions' workspaces belong)", claude, codex)
	}
}

func TestRestoreKeepsExistingFilesAndTheOldDatabase(t *testing.T) {
	src := fixture(t)
	var buf bytes.Buffer
	if _, err := Write(context.Background(), &buf, src, ""); err != nil {
		t.Fatal(err)
	}
	// Same machine: the plugin was edited since, and the database is in use.
	plugin := filepath.Join(src.Home, ".lectern", "plugins", "p", "plugin.json")
	write(t, plugin, "newer")
	write(t, src.DBPath+"-wal", "stale wal")
	res, err := Restore(&buf, "", Target{DBPath: src.DBPath, Home: src.Home})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, plugin) != "newer" || res.Kept == 0 {
		t.Fatalf("existing file was replaced (kept %d)", res.Kept)
	}
	if _, err := os.Stat(src.DBPath + ".before-restore"); err != nil {
		t.Fatal("old database not kept aside")
	}
	if _, err := os.Stat(src.DBPath + "-wal"); err == nil {
		t.Fatal("old -wal left next to the restored database")
	}
}

func TestRestoreRefusesAWrongPassphrase(t *testing.T) {
	var buf bytes.Buffer
	if _, err := Write(context.Background(), &buf, fixture(t), "right"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, pass := range []string{"", "wrong"} {
		_, err := Restore(bytes.NewReader(buf.Bytes()), pass, Target{DBPath: filepath.Join(dir, "x.db"), Home: dir})
		if !errors.Is(err, ErrPassphrase) {
			t.Fatalf("passphrase %q: err = %v", pass, err)
		}
	}
}

func TestRestoreRefusesPathsThatClimbOut(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "files/../../evil", Size: 1, Mode: 0o600, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	dir := t.TempDir()
	if _, err := Restore(&buf, "", Target{DBPath: filepath.Join(dir, "x.db"), Home: dir}); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("err = %v", err)
	}
}

func TestCodexCwdReadsPastLongInstructions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.jsonl")
	write(t, p, `{"type":"session_meta","payload":{"cwd":"/w/a \"q\"","base_instructions":"`+strings.Repeat("x", 200<<10)+`"}}`+"\n")
	if got := codexCwd(p); got != `/w/a "q"` {
		t.Fatalf("cwd = %q", got)
	}
}

// fakeGitHub is just enough of the REST API: a repository, releases with
// assets, deletes.
type fakeGitHub struct {
	mu       sync.Mutex
	repo     bool
	private  bool
	releases map[int64]*Release
	blobs    map[int64][]byte
	next     int64
	srv      *httptest.Server
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{releases: map[int64]*Release{}, blobs: map[int64][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(code int, v any) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	p := r.URL.Path
	switch {
	case r.Method == "GET" && p == "/repos/me/backups":
		if !f.repo {
			reply(404, map[string]string{"message": "Not Found"})
			return
		}
		reply(200, map[string]bool{"private": f.private})
	case r.Method == "GET" && p == "/user":
		reply(200, map[string]string{"login": "me"})
	case r.Method == "POST" && p == "/user/repos":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.repo, f.private = true, body["private"] == true
		reply(201, map[string]any{})
	case r.Method == "GET" && p == "/repos/me/backups/commits":
		reply(200, []any{})
	case r.Method == "POST" && p == "/repos/me/backups/releases":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		f.next++
		rel := &Release{ID: f.next, Tag: body["tag_name"], Name: body["name"], Body: body["body"], CreatedAt: time.Now()}
		f.releases[rel.ID] = rel
		reply(201, rel)
	case r.Method == "POST" && strings.HasPrefix(p, "/repos/me/backups/releases/"):
		var id int64
		fmt.Sscanf(strings.TrimPrefix(p, "/repos/me/backups/releases/"), "%d/assets", &id)
		data, _ := io.ReadAll(r.Body)
		f.next++
		a := Asset{ID: f.next, Name: r.URL.Query().Get("name"), Size: int64(len(data)), URL: f.srv.URL + fmt.Sprintf("/assets/%d", f.next)}
		f.blobs[a.ID] = data
		f.releases[id].Assets = append(f.releases[id].Assets, a)
		reply(201, a)
	case r.Method == "GET" && p == "/repos/me/backups/releases":
		var out []*Release
		for _, rel := range f.releases {
			out = append(out, rel)
		}
		reply(200, out)
	case r.Method == "GET" && strings.HasPrefix(p, "/assets/"):
		var id int64
		fmt.Sscanf(p, "/assets/%d", &id)
		w.Write(f.blobs[id])
	case r.Method == "DELETE" && strings.HasPrefix(p, "/repos/me/backups/releases/"):
		var id int64
		fmt.Sscanf(strings.TrimPrefix(p, "/repos/me/backups/releases/"), "%d", &id)
		delete(f.releases, id)
		w.WriteHeader(204)
	case r.Method == "DELETE" && strings.HasPrefix(p, "/repos/me/backups/git/refs/tags/"):
		w.WriteHeader(204)
	default:
		reply(404, map[string]string{"message": r.Method + " " + p})
	}
}

type memStore map[string]string

func (m memStore) Setting(k string) string      { return m[k] }
func (m memStore) SetSetting(k, v string) error { m[k] = v; return nil }

func TestRunnerUploadsEncryptedPrunesAndRestoresLatest(t *testing.T) {
	gh := newFakeGitHub(t)
	src := fixture(t)
	st := memStore{}
	client := func(_ context.Context, repo string) (*GitHub, error) {
		// uploads.<host> is derived from api.<host>; the fake serves both.
		return &GitHub{Repo: repo, Token: "t", API: gh.srv.URL}, nil
	}
	r := &Runner{Store: st, Source: func(context.Context) (Source, error) { return src, nil }, GitHub: client}

	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("ran with no repository configured")
	}
	if err := r.SaveSettings(Settings{Repo: "https://github.com/me/backups.git", Keep: 2, EveryHours: 24}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("uploaded without a passphrase: %v", err)
	}
	s := r.Settings()
	s.Passphrase = "pw"
	r.SaveSettings(s)
	transcript := filepath.Join(src.Home, ".claude", "projects")
	var full string
	for i := 0; i < 3; i++ {
		status, err := r.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.Tag == "" || status.Sessions != 2 {
			t.Fatalf("status = %+v", status)
		}
		if i == 0 {
			full = status.Tag
			if status.Full != full {
				t.Fatalf("first backup is not full: %+v", status)
			}
		} else if status.Full != full || status.Files != 2 {
			// The database, and the one transcript touched below.
			t.Fatalf("backup %d: %+v, want changes since %s only", i, status, full)
		}
		time.Sleep(1100 * time.Millisecond) // tags are per second
		filepath.Walk(transcript, func(p string, _ os.FileInfo, _ error) error {
			if strings.HasSuffix(p, "c1.jsonl") {
				write(t, p, fmt.Sprintf("turn %d\n", i+1))
			}
			return nil
		})
	}
	if !gh.private {
		t.Fatal("repository was not created private")
	}
	// Keep 2 holds the two newest (both changes-only) and the full backup they need.
	if len(gh.releases) != 3 {
		t.Fatalf("kept %d backups, want 3", len(gh.releases))
	}
	if r.Due(time.Now()) || !r.Due(time.Now().Add(25*time.Hour)) {
		t.Fatal("schedule wrong")
	}

	c, _ := client(context.Background(), "me/backups")
	rel, err := c.Find(context.Background(), "latest")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Base() != full {
		t.Fatalf("latest builds on %q, want %q", rel.Base(), full)
	}
	dir := t.TempDir()
	res, err := c.RestoreRelease(context.Background(), rel, "pw", Target{DBPath: filepath.Join(dir, "lectern.db"), Home: dir}, dir, nil)
	if err != nil || !res.Database {
		t.Fatalf("restore latest: %+v %v", res, err)
	}
	// From the full backup:
	if got := read(t, filepath.Join(dir, ".lectern", "plugins", "p", "plugin.json")); got != `{"id":"p"}` {
		t.Fatalf("plugin = %q", got)
	}
	// From the newest changes, laid over the full one:
	var got string
	filepath.Walk(filepath.Join(dir, ".claude"), func(p string, _ os.FileInfo, _ error) error {
		if strings.HasSuffix(p, "c1.jsonl") {
			got = read(t, p)
		}
		return nil
	})
	if got != "turn 2\n" {
		t.Fatalf("transcript = %q, want the newest", got)
	}
}

func TestEnsureRepoRefusesAPublicRepository(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.repo, gh.private = true, false
	_, err := (&GitHub{Repo: "me/backups", Token: "t", API: gh.srv.URL}).EnsureRepo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "public") {
		t.Fatalf("err = %v", err)
	}
}
