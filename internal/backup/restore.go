package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

// ErrPassphrase means the archive is encrypted and the passphrase given does
// not open it (or none was given).
var ErrPassphrase = errors.New("backup: wrong or missing passphrase")

// Target says where a restore puts things on this machine.
type Target struct {
	// DBPath receives the database. An existing database is moved aside to
	// DBPath + ".before-restore" first, never deleted.
	DBPath string
	// Home replaces the backed-up home folder in every saved path, so a
	// restore onto a machine with a different user name lands correctly.
	Home string
	// MediaDir, when set, replaces the backed-up media folder.
	MediaDir string
	// Extra receives what has no fixed place here: the saved LECTERN_*
	// settings and remote targets' transcripts.
	Extra string
	// Overwrite replaces files that already exist; otherwise they are kept
	// and counted as skipped.
	Overwrite bool
	// SkipDB leaves the database out (a full backup under a differential,
	// whose own database is newer).
	SkipDB bool
	// Replace names files an earlier layer of this same restore wrote, which
	// a later layer may replace even without Overwrite.
	Replace map[string]bool
}

// Result says what a restore did.
type Result struct {
	Manifest *Manifest
	Files    int
	Kept     int // already present, left alone
	Database bool
	EnvFile  string
	Remote   []string
	// Placed is every file this restore wrote.
	Placed map[string]bool
}

// Open returns the plain tar.gz stream of an archive, decrypting it when it
// is age-encrypted.
func Open(r io.Reader, passphrase string) (io.Reader, error) {
	br := bufio.NewReader(r)
	head, _ := br.Peek(len("age-encryption.org/"))
	if !bytes.Equal(head, []byte("age-encryption.org/")) {
		return br, nil
	}
	if passphrase == "" {
		return nil, ErrPassphrase
	}
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	out, err := age.Decrypt(br, id)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return nil, ErrPassphrase
		}
		return nil, err
	}
	return out, nil
}

// Restore unpacks an archive onto this machine. Stop the Lectern server
// first: the database is replaced underneath it otherwise.
func Restore(r io.Reader, passphrase string, to Target) (*Result, error) {
	plain, err := Open(r, passphrase)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		return nil, fmt.Errorf("backup: not a Lectern backup: %w", err)
	}
	// The manifest is written last, so the old home and media folders are
	// not known until the end: entries are staged under their saved paths
	// in a scratch folder, then moved into place.
	if err := os.MkdirAll(filepath.Dir(to.DBPath), 0o700); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(to.DBPath), ".lectern-restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	tr := tar.NewReader(gz)
	var m *Manifest
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("backup: archive damaged: %w", err)
		}
		name, ok := cleanName(h.Name)
		if !ok {
			return nil, fmt.Errorf("backup: unsafe path in archive: %q", h.Name)
		}
		if name == "manifest.json" {
			m = &Manifest{}
			if err := json.NewDecoder(tr).Decode(m); err != nil {
				return nil, fmt.Errorf("backup: manifest: %w", err)
			}
			continue
		}
		dst := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		switch h.Typeflag {
		case tar.TypeSymlink:
			if err := os.Symlink(h.Linkname, dst); err != nil {
				return nil, err
			}
		case tar.TypeReg:
			if err := writeFrom(dst, tr, os.FileMode(h.Mode).Perm()|0o600, h); err != nil {
				return nil, err
			}
		}
	}
	if m == nil {
		return nil, errors.New("backup: archive has no manifest (incomplete upload?)")
	}
	if m.Format > FormatVersion {
		return nil, fmt.Errorf("backup: made by a newer Lectern (format %d); upgrade first", m.Format)
	}
	return place(stage, m, to)
}

func writeFrom(dst string, r io.Reader, mode os.FileMode, h *tar.Header) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, h.ModTime, h.ModTime)
}

func place(stage string, m *Manifest, to Target) (*Result, error) {
	res := &Result{Manifest: m, Placed: map[string]bool{}}
	if db := filepath.Join(stage, "db", "lectern.db"); exists(db) && !to.SkipDB {
		if err := os.MkdirAll(filepath.Dir(to.DBPath), 0o700); err != nil {
			return nil, err
		}
		if exists(to.DBPath) {
			if err := os.Rename(to.DBPath, to.DBPath+".before-restore"); err != nil {
				return nil, err
			}
		}
		// A -wal or -shm left from the old database would be replayed into
		// the restored one.
		for _, suffix := range []string{"-wal", "-shm"} {
			if exists(to.DBPath + suffix) {
				if err := os.Rename(to.DBPath+suffix, to.DBPath+".before-restore"+suffix); err != nil {
					return nil, err
				}
			}
		}
		if err := os.Rename(db, to.DBPath); err != nil {
			return nil, err
		}
		res.Database = true
	}
	extra := to.Extra
	if extra == "" {
		extra = filepath.Join(filepath.Dir(to.DBPath), "restored")
	}
	if env := filepath.Join(stage, "env", "lectern.env"); exists(env) {
		res.EnvFile = filepath.Join(extra, "lectern.env")
		if err := moveFile(env, res.EnvFile, true); err != nil {
			return nil, err
		}
	}
	if entries, err := os.ReadDir(filepath.Join(stage, "remote")); err == nil {
		for _, e := range entries {
			dst := filepath.Join(extra, "remote", e.Name())
			if err := moveFile(filepath.Join(stage, "remote", e.Name()), dst, true); err != nil {
				return nil, err
			}
			res.Remote = append(res.Remote, dst)
		}
	}
	files := filepath.Join(stage, "files")
	err := filepath.Walk(files, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		saved := "/" + filepath.ToSlash(strings.TrimPrefix(p, files+string(filepath.Separator)))
		dst := filepath.FromSlash(remap(saved, m, to))
		replace := to.Overwrite || to.Replace[dst]
		if exists(dst) && !replace {
			res.Kept++
			return nil
		}
		if err := moveFile(p, dst, replace); err != nil {
			return err
		}
		res.Placed[dst] = true
		res.Files++
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return res, nil
}

// remap moves a saved path from the backed-up machine's folders to this one's.
func remap(saved string, m *Manifest, to Target) string {
	swap := func(p, from, into string) (string, bool) {
		from = filepath.ToSlash(from)
		if from == "" || into == "" {
			return p, false
		}
		if p == from || strings.HasPrefix(p, from+"/") {
			return filepath.ToSlash(into) + strings.TrimPrefix(p, from), true
		}
		return p, false
	}
	if p, ok := swap(saved, m.MediaDir, to.MediaDir); ok {
		return p
	}
	p, _ := swap(saved, m.Home, to.Home)
	return p
}

func moveFile(src, dst string, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if overwrite {
		os.Remove(dst)
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Different file systems: copy.
	if fi, err := os.Lstat(src); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		to, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(to, dst)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, st.ModTime(), st.ModTime())
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// cleanName rejects absolute paths and any that climb out with "..".
func cleanName(name string) (string, bool) {
	if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", false
		}
	}
	c := filepath.ToSlash(filepath.Clean(name))
	return c, c != "." && c != ""
}

// Verify reads an archive through to the end without unpacking it: it opens
// with the passphrase, every entry is intact, and the manifest is there.
func Verify(r io.Reader, passphrase string) (*Manifest, error) {
	plain, err := Open(r, passphrase)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		return nil, fmt.Errorf("backup: not a Lectern backup: %w", err)
	}
	tr := tar.NewReader(gz)
	var m *Manifest
	db := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("backup: archive damaged: %w", err)
		}
		switch h.Name {
		case "manifest.json":
			m = &Manifest{}
			if err := json.NewDecoder(tr).Decode(m); err != nil {
				return nil, fmt.Errorf("backup: manifest: %w", err)
			}
		case "db/lectern.db":
			db = h.Size > 0
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return nil, fmt.Errorf("backup: archive damaged: %w", err)
		}
	}
	if m == nil {
		return nil, errors.New("backup: archive has no manifest")
	}
	if m.DBPath != "" && !db {
		return nil, errors.New("backup: archive has no database")
	}
	return m, nil
}
