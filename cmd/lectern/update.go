package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/cmd/lectern/localruntime"
	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// `lectern update` replaces this program with a GitHub release, the same
// archive and checksum check install.sh uses. It replaces the file that is
// actually running (os.Executable, symlinks resolved), which is right for
// every layout Lectern ships: ~/.local/bin/lectern from install.sh, a
// go-installed binary, and the desktop installer's ~/.local/bin/lectern
// wrapper script, which execs ~/.local/lib/lectern/client: the running
// program is the client, and the wrapper keeps working untouched.

const releaseRepo = "JeremiahM37/lectern"

type updater struct {
	exe        string // the running program, resolved
	latestURL  string // GitHub's "latest release" API
	releaseURL string // where a tag's assets live; "%s" is the tag
	goos, arch string
	client     *http.Client
	out        io.Writer
	installed  string // the tag run installed
}

func newUpdater(out io.Writer) (*updater, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	u := &updater{
		exe:        exe,
		latestURL:  "https://api.github.com/repos/" + releaseRepo + "/releases/latest",
		releaseURL: "https://github.com/" + releaseRepo + "/releases/download/%s",
		goos:       runtime.GOOS, arch: runtime.GOARCH,
		client: &http.Client{Timeout: 5 * time.Minute},
		out:    out,
	}
	// Tests (and mirrors) point these elsewhere, like install.sh's
	// LECTERN_RELEASE_BASE.
	if base := os.Getenv("LECTERN_RELEASE_BASE"); base != "" {
		u.releaseURL = strings.TrimRight(base, "/")
		u.latestURL = u.releaseURL + "/latest.json"
	}
	return u, nil
}

func updateCommand(args []string) error {
	check, want := false, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--check":
			check = true
		case "--version":
			if i+1 >= len(args) {
				return errors.New("--version needs a release, for example v2.7.0")
			}
			i++
			want = args[i]
		default:
			return errors.New("usage: lectern update [--check] [--version vX.Y.Z]")
		}
	}
	u, err := newUpdater(os.Stdout)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	updated, err := u.run(ctx, version.Version, want, check)
	if err != nil || !updated {
		return err
	}
	// A running private Lectern keeps its old build until it restarts.
	if ep, ok := localruntime.Peek(ctx); ok && ep.Build.Version != "" && ep.Build.Version != strings.TrimPrefix(u.installed, "v") {
		fmt.Printf("Your private Lectern is still running %s. To switch: lectern local stop && lectern up\n", ep.Build.Version)
	}
	return nil
}

// managedBy names the package manager that owns exe, or "" when nobody does
// and replacing the file is ours to do.
func managedBy(exe string) (tool, command string) {
	lower := strings.ToLower(filepath.ToSlash(exe))
	switch {
	case strings.Contains(lower, "/caskroom/") || strings.Contains(lower, "/cellar/") || strings.Contains(lower, "/homebrew/"):
		return "Homebrew", "brew upgrade --cask lectern"
	case strings.Contains(lower, "/scoop/"):
		return "Scoop", "scoop update lectern"
	}
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("dpkg"); err == nil && exec.Command("dpkg", "-S", exe).Run() == nil {
			return "your package manager (.deb)", "sudo apt-get install --only-upgrade lectern, or download the new .deb from the release page"
		}
		if _, err := exec.LookPath("rpm"); err == nil && exec.Command("rpm", "-qf", exe).Run() == nil {
			return "your package manager (.rpm)", "sudo dnf upgrade lectern, or download the new .rpm from the release page"
		}
	}
	return "", ""
}

func (u *updater) run(ctx context.Context, current, want string, checkOnly bool) (bool, error) {
	tag := want
	if tag == "" {
		latest, err := u.latest(ctx)
		if err != nil {
			return false, fmt.Errorf("could not find the latest release: %w (check your connection, or pass --version vX.Y.Z)", err)
		}
		tag = latest
		older, known := version.Older(version.Info{Version: current}, version.Info{Version: tag})
		if strings.TrimPrefix(current, "v") == strings.TrimPrefix(tag, "v") || known && !older {
			fmt.Fprintf(u.out, "lectern %s is the latest release.\n", current)
			return false, nil
		}
	}
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if checkOnly {
		fmt.Fprintf(u.out, "lectern %s is available (this is %s). Run: lectern update\n", tag, current)
		return false, nil
	}
	if tool, command := managedBy(u.exe); tool != "" {
		return false, fmt.Errorf("this lectern was installed with %s; update it with: %s", tool, command)
	}
	archive := fmt.Sprintf("lectern_%s_%s.tar.gz", u.goos, u.arch)
	if u.goos == "windows" {
		archive = fmt.Sprintf("lectern_%s_%s.zip", u.goos, u.arch)
	}
	base := u.releaseURL
	if strings.Contains(base, "%s") {
		base = fmt.Sprintf(base, tag)
	}
	fmt.Fprintf(u.out, "Downloading lectern %s (%s)…\n", tag, archive)
	data, err := u.get(ctx, base+"/"+archive)
	if err != nil {
		return false, err
	}
	sums, err := u.get(ctx, base+"/checksums.txt")
	if err != nil {
		return false, err
	}
	if err := verifyChecksum(data, sums, archive); err != nil {
		return false, err
	}
	binary, err := extractLectern(data, u.goos)
	if err != nil {
		return false, err
	}
	if err := replaceExecutable(u.exe, binary, u.goos); err != nil {
		var perm *os.PathError
		if errors.As(err, &perm) && errors.Is(err, os.ErrPermission) {
			return false, fmt.Errorf("cannot write %s: %w. Run it with the rights to that folder (for example: sudo lectern update)", u.exe, err)
		}
		return false, err
	}
	fmt.Fprintf(u.out, "Updated %s from %s to %s.\n", u.exe, current, tag)
	u.installed = tag
	return true, nil
}

func (u *updater) latest(ctx context.Context) (string, error) {
	data, err := u.get(ctx, u.latestURL)
	if err != nil {
		return "", err
	}
	var release struct {
		Tag string `json:"tag_name"`
	}
	if err := json.Unmarshal(data, &release); err != nil || release.Tag == "" {
		return "", errors.New("the release list had no tag")
	}
	return release.Tag, nil
}

func (u *updater) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lectern-update/"+version.Version)
	res, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 512<<20))
}

func verifyChecksum(data, sums []byte, name string) error {
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			want = fields[0]
		}
	}
	got := sha256.Sum256(data)
	if want == "" || !strings.EqualFold(want, hex.EncodeToString(got[:])) {
		return fmt.Errorf("checksum mismatch for %s; nothing was changed", name)
	}
	return nil
}

func extractLectern(data []byte, goos string) ([]byte, error) {
	name := "lectern"
	if goos == "windows" {
		name = "lectern.exe"
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) == name {
				r, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer r.Close()
				return io.ReadAll(io.LimitReader(r, 512<<20))
			}
		}
		return nil, errors.New("the release archive has no " + name)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, errors.New("the release archive has no " + name)
		}
		if filepath.Base(h.Name) == name && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 512<<20))
		}
	}
}

// replaceExecutable swaps in the new program next to the old one and only
// after it answers `version`, so a bad download never leaves a broken
// lectern behind. Windows cannot overwrite a running .exe, but can rename it.
func replaceExecutable(exe string, binary []byte, goos string) error {
	dir := filepath.Dir(exe)
	next := filepath.Join(dir, "."+filepath.Base(exe)+".next")
	if err := os.WriteFile(next, binary, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(next, 0o755); err != nil {
		_ = os.Remove(next)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, next, "version").CombinedOutput(); err != nil {
		_ = os.Remove(next)
		return fmt.Errorf("the downloaded lectern did not run (%v: %s); nothing was changed", err, strings.TrimSpace(string(out)))
	}
	if goos == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			_ = os.Remove(next)
			return err
		}
	}
	if err := os.Rename(next, exe); err != nil {
		_ = os.Remove(next)
		return err
	}
	return nil
}
