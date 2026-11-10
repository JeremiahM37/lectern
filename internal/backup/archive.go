// Package backup writes everything a Lectern server knows into one archive
// and puts it back: the database (sessions, tasks, projects, workflows,
// approvals, settings), posted media, the ~/.lectern folder (plugins, hooks,
// Scribe chats), the agents' own chat transcripts for every workspace a
// session ran in, and the server's LECTERN_* configuration.
//
// The archive is a gzip'd tar, encrypted with age (a passphrase, scrypt) when
// one is given, so a copy kept off this machine is useless to anyone without
// the passphrase and still opens with the stock `age -d` if Lectern itself is
// gone. Layout inside the tar:
//
//	manifest.json       what was saved, from where, and what could not be
//	db/lectern.db       a consistent snapshot (VACUUM INTO), safe while serving
//	files/<abs path>    files from this machine, at their original paths
//	remote/<target>/... transcripts read from SSH targets
//	env/lectern.env     the server's LECTERN_* settings, secrets included
package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	_ "modernc.org/sqlite"
)

// FormatVersion is bumped when the layout changes in a way Restore must know.
const FormatVersion = 1

// Workspace is one place agents ran: the transcripts Lectern shows as a
// session's chat live under the agent's home, keyed by this folder.
type Workspace struct {
	Agent  string `json:"agent"`
	Dir    string `json:"dir"`
	Target string `json:"target"`
}

// Source says what to save.
type Source struct {
	DBPath   string
	MediaDir string
	Home     string
	// Workspaces on this machine; their Claude and Codex transcripts are saved.
	Workspaces []Workspace
	// Remote returns a gzip'd tar of one SSH target's transcripts for its
	// workspaces, and a warning when some were left out. nil skips remote
	// targets.
	Remote func(ctx context.Context, target string, ws []Workspace) (data []byte, warning string, err error)
	// RemoteWorkspaces are grouped by target name and handed to Remote.
	RemoteWorkspaces map[string][]Workspace
	// Env is saved as env/lectern.env (KEY=VALUE lines).
	Env []string
	// Version and Host are recorded in the manifest.
	Version, Host string
	// TempDir holds the database snapshot while it is copied; "" is the default.
	TempDir string
	// Base and Since make a differential backup: only files changed after
	// Since are saved, and Restore lays it over the full backup Base. The
	// database and settings are always saved whole.
	Base  string
	Since time.Time
}

// Manifest describes an archive.
type Manifest struct {
	Format    int       `json:"format"`
	CreatedAt time.Time `json:"created_at"`
	Version   string    `json:"lectern_version"`
	Host      string    `json:"host"`
	Home      string    `json:"home"`
	DBPath    string    `json:"db_path"`
	MediaDir  string    `json:"media_dir,omitempty"`
	// Base is the full backup this one holds the changes since; "" = full.
	Base     string    `json:"base,omitempty"`
	Since    time.Time `json:"since,omitzero"`
	Sessions int       `json:"sessions"`
	Files    int       `json:"files"`
	Bytes    int64     `json:"bytes"`
	// Skipped lists what could not be saved and why; a backup with entries
	// here still holds everything else.
	Skipped []string `json:"skipped,omitempty"`
}

// Write saves src to w. With a passphrase the stream is age-encrypted.
func Write(ctx context.Context, w io.Writer, src Source, passphrase string) (*Manifest, error) {
	if src.Home == "" {
		return nil, errors.New("backup: no home folder")
	}
	out := w
	var enc io.WriteCloser
	if passphrase != "" {
		r, err := age.NewScryptRecipient(passphrase)
		if err != nil {
			return nil, err
		}
		if enc, err = age.Encrypt(w, r); err != nil {
			return nil, err
		}
		out = enc
	}
	gz := gzip.NewWriter(out)
	b := &builder{tw: tar.NewWriter(gz), seen: map[string]bool{}, m: &Manifest{
		Format: FormatVersion, CreatedAt: time.Now().UTC(), Version: src.Version, Host: src.Host,
		Home: src.Home, DBPath: src.DBPath, MediaDir: src.MediaDir, Base: src.Base, Since: src.Since,
	}, since: src.Since}
	if err := b.write(ctx, src); err != nil {
		return nil, err
	}
	// The manifest goes last so it can count everything; Restore reads the
	// whole stream anyway.
	raw, _ := json.MarshalIndent(b.m, "", "  ")
	if err := b.bytes("manifest.json", raw, 0o600); err != nil {
		return nil, err
	}
	if err := b.tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if enc != nil {
		if err := enc.Close(); err != nil {
			return nil, err
		}
	}
	return b.m, nil
}

type builder struct {
	tw    *tar.Writer
	m     *Manifest
	seen  map[string]bool
	since time.Time
}

func (b *builder) skip(format string, args ...any) {
	b.m.Skipped = append(b.m.Skipped, fmt.Sprintf(format, args...))
}

func (b *builder) write(ctx context.Context, src Source) error {
	if src.DBPath != "" {
		if err := b.database(ctx, src); err != nil {
			return err
		}
	}
	if len(src.Env) > 0 {
		env := strings.Join(src.Env, "\n") + "\n"
		if err := b.bytes("env/lectern.env", []byte(env), 0o600); err != nil {
			return err
		}
	}
	dirs := []string{filepath.Join(src.Home, ".lectern")}
	if src.MediaDir != "" {
		dirs = append(dirs, src.MediaDir)
	}
	for _, f := range []string{".claude/history.jsonl", ".codex/history.jsonl", ".codex/session_index.jsonl"} {
		dirs = append(dirs, filepath.Join(src.Home, f))
	}
	claude, codex := transcriptPlaces(src.Home, src.Workspaces)
	dirs = append(dirs, claude...)
	for _, d := range dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := b.tree(d); err != nil {
			return err
		}
	}
	if len(codex) > 0 {
		if err := b.codex(ctx, filepath.Join(src.Home, ".codex", "sessions"), codex); err != nil {
			return err
		}
	}
	if src.Remote != nil {
		targets := make([]string, 0, len(src.RemoteWorkspaces))
		for t := range src.RemoteWorkspaces {
			targets = append(targets, t)
		}
		sort.Strings(targets)
		for _, t := range targets {
			data, warning, err := src.Remote(ctx, t, src.RemoteWorkspaces[t])
			if err != nil {
				b.skip("transcripts on %s: %v", t, err)
				continue
			}
			if warning != "" {
				b.skip("transcripts on %s: %s", t, warning)
			}
			if err := b.bytes("remote/"+safeName(t)+".tar.gz", data, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

// database copies a consistent snapshot. VACUUM INTO reads inside one
// transaction, so a server writing at the same time is fine and the copy has
// no -wal to go with it.
func (b *builder) database(ctx context.Context, src Source) error {
	if _, err := os.Stat(src.DBPath); err != nil {
		return fmt.Errorf("backup: database: %w", err)
	}
	dir, err := os.MkdirTemp(src.TempDir, ".lectern-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	snap := filepath.Join(dir, "lectern.db")
	db, err := sql.Open("sqlite", "file:"+src.DBPath+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", snap); err != nil {
		return fmt.Errorf("backup: snapshot database: %w", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&n); err == nil {
		b.m.Sessions = n
	}
	return b.file("db/lectern.db", snap)
}

// transcriptPlaces names where each workspace's transcripts live: Claude
// keeps one folder per workspace; Codex files are dated, so the workspaces
// are returned for codex() to match against each file's recorded cwd.
func transcriptPlaces(home string, ws []Workspace) (claude []string, codex map[string]bool) {
	slug := regexp.MustCompile(`[^a-zA-Z0-9]`)
	seen := map[string]bool{}
	codex = map[string]bool{}
	for _, w := range ws {
		if !filepath.IsAbs(w.Dir) {
			continue
		}
		dirs := []string{w.Dir}
		if real, err := filepath.EvalSymlinks(w.Dir); err == nil && real != w.Dir {
			dirs = append(dirs, real)
		}
		for _, d := range dirs {
			switch w.Agent {
			case "codex":
				codex[d] = true
			case "claude":
				p := filepath.Join(home, ".claude", "projects", slug.ReplaceAllString(d, "-"))
				if !seen[p] {
					seen[p] = true
					claude = append(claude, p)
				}
			}
		}
	}
	return claude, codex
}

// codex saves the rollout files whose session_meta names one of dirs.
func (b *builder) codex(ctx context.Context, root string, dirs map[string]bool) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		if cwd := codexCwd(p); cwd != "" && dirs[cwd] {
			return b.file("files"+filepath.ToSlash(p), p)
		}
		return nil
	})
}

func codexCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 64<<10).ReadSlice('\n')
	if err != nil && !errors.Is(err, bufio.ErrBufferFull) && !errors.Is(err, io.EOF) {
		return ""
	}
	// session_meta carries base instructions, which can outgrow the buffer;
	// cwd comes before them, so pull it out with a match instead of a parse.
	m := regexp.MustCompile(`"cwd":"((?:[^"\\]|\\.)*)"`).FindSubmatch(line)
	if m == nil {
		return ""
	}
	var cwd string
	if json.Unmarshal(append(append([]byte{'"'}, m[1]...), '"'), &cwd) != nil {
		return ""
	}
	return cwd
}

// tree saves a file or a folder, recursively. A missing path is not an error:
// not every install has media or Codex.
func (b *builder) tree(root string) error {
	if _, err := os.Lstat(root); err != nil {
		return nil
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			b.skip("%s: %v", p, err)
			return nil
		}
		if d.IsDir() {
			// Caches rebuild themselves; a browser profile's cache is most of its size.
			switch d.Name() {
			case "Cache", "Code Cache", "GPUCache", "Service Worker", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		name := "files" + filepath.ToSlash(p)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return b.link(name, p)
		case d.Type().IsRegular():
			return b.file(name, p)
		}
		return nil // sockets, fifos: nothing to keep
	})
}

func (b *builder) link(name, path string) error {
	if b.seen[name] {
		return nil
	}
	b.seen[name] = true
	to, err := os.Readlink(path)
	if err != nil {
		b.skip("%s: %v", path, err)
		return nil
	}
	return b.tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: to, Mode: 0o777, ModTime: time.Now()})
}

// file copies one file. A transcript may still be growing: the header takes
// the size at open, exactly that many bytes are copied, and a file that
// shrank underneath is padded so the tar stays readable.
func (b *builder) file(name, path string) error {
	if b.seen[name] {
		return nil
	}
	b.seen[name] = true
	f, err := os.Open(path)
	if err != nil {
		b.skip("%s: %v", path, err)
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		b.skip("%s: %v", path, err)
		return nil
	}
	if !b.since.IsZero() && strings.HasPrefix(name, "files/") && !st.ModTime().After(b.since) {
		return nil // unchanged since the full backup
	}
	if err := b.tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: name, Size: st.Size(), Mode: int64(st.Mode().Perm()), ModTime: st.ModTime(),
	}); err != nil {
		return err
	}
	n, err := io.CopyN(b.tw, f, st.Size())
	if n < st.Size() {
		if _, perr := io.CopyN(b.tw, zeros{}, st.Size()-n); perr != nil {
			return perr
		}
		if err != nil && !errors.Is(err, io.EOF) {
			b.skip("%s: %v", path, err)
		}
	}
	b.m.Files++
	b.m.Bytes += st.Size()
	return nil
}

func (b *builder) bytes(name string, data []byte, mode int64) error {
	if err := b.tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: name, Size: int64(len(data)), Mode: mode, ModTime: time.Now(),
	}); err != nil {
		return err
	}
	_, err := b.tw.Write(data)
	return err
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func safeName(s string) string {
	return regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(s, "_")
}
