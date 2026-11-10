package backup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Backups go to a private GitHub repository as release assets, one release
// per backup. Git itself refuses files over 100 MB and keeps every version
// forever; release assets take up to 2 GiB each and an old one is deleted
// outright, so the repository stays the size of the backups it keeps.
const (
	tagPrefix = "backup-"
	// partSize splits an archive under GitHub's 2 GiB asset limit.
	partSize = 1900 << 20
)

// GitHub talks to one repository.
type GitHub struct {
	Repo  string // owner/name
	Token string
	API   string // https://api.github.com unless a test points elsewhere
	HTTP  *http.Client
}

// Release is one backup.
type Release struct {
	ID        int64     `json:"id"`
	Tag       string    `json:"tag_name"`
	Name      string    `json:"name"`
	URL       string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	Body      string    `json:"body"`
	Assets    []Asset   `json:"assets"`
}

// Base is the full backup a differential one builds on; "" for a full one.
func (r Release) Base() string {
	for _, line := range strings.Split(r.Body, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Base: "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Asset is one uploaded file of a release.
type Asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

// Size is the backup's total size.
func (r Release) Size() int64 {
	var n int64
	for _, a := range r.Assets {
		n += a.Size
	}
	return n
}

// GitHubToken finds a token: LECTERN_BACKUP_GITHUB_TOKEN, then GH_TOKEN or
// GITHUB_TOKEN, then the GitHub CLI's own login (`gh auth token`).
func GitHubToken(ctx context.Context) (token, source string) {
	for _, k := range []string{"LECTERN_BACKUP_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v, k
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if v := strings.TrimSpace(string(out)); err == nil && v != "" {
		return v, "gh auth"
	}
	return "", ""
}

func (g *GitHub) api() string {
	if g.API != "" {
		return strings.TrimRight(g.API, "/")
	}
	return "https://api.github.com"
}

func (g *GitHub) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return http.DefaultClient
}

// APIError is a non-2xx answer.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("github: %d %s", e.Status, e.Message) }

func (g *GitHub) do(ctx context.Context, method, u string, body io.Reader, size int64, contentType string, out any) error {
	if g.Token == "" {
		return errors.New("github: no token (sign in with `gh auth login` or set LECTERN_BACKUP_GITHUB_TOKEN)")
	}
	if !strings.HasPrefix(u, "http") {
		u = g.api() + u
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if size >= 0 && body != nil {
		req.ContentLength = size
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		json.Unmarshal(raw, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(raw))
		}
		return &APIError{Status: resp.StatusCode, Message: e.Message}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (g *GitHub) jsonBody(v any) (io.Reader, int64) {
	raw, _ := json.Marshal(v)
	return bytes.NewReader(raw), int64(len(raw))
}

func isStatus(err error, status int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == status
}

// EnsureRepo makes the repository exist, private and with a first commit
// (a release needs one). It refuses a public repository.
func (g *GitHub) EnsureRepo(ctx context.Context) (created bool, err error) {
	owner, name, ok := strings.Cut(g.Repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return false, fmt.Errorf("github: repository must be owner/name, not %q", g.Repo)
	}
	var repo struct {
		Private bool `json:"private"`
	}
	err = g.do(ctx, "GET", "/repos/"+g.Repo, nil, -1, "", &repo)
	if isStatus(err, 404) {
		var me struct {
			Login string `json:"login"`
		}
		if err := g.do(ctx, "GET", "/user", nil, -1, "", &me); err != nil {
			return false, err
		}
		path := "/user/repos"
		if !strings.EqualFold(me.Login, owner) {
			path = "/orgs/" + url.PathEscape(owner) + "/repos"
		}
		body, size := g.jsonBody(map[string]any{
			"name": name, "private": true, "auto_init": true,
			"description": "Lectern backups (encrypted). Restore with `lectern backup restore latest`.",
		})
		if err := g.do(ctx, "POST", path, body, size, "application/json", nil); err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !repo.Private {
		return false, fmt.Errorf("github: %s is public; backups go only to a private repository", g.Repo)
	}
	// An empty repository has no commit to tag; give it a README.
	if err := g.do(ctx, "GET", "/repos/"+g.Repo+"/commits?per_page=1", nil, -1, "", nil); isStatus(err, 409) {
		body, size := g.jsonBody(map[string]any{
			"message": "Lectern backups",
			"content": base64.StdEncoding.EncodeToString([]byte("# Lectern backups\n\nEach release is one encrypted backup. Restore with `lectern backup restore latest`.\n")),
		})
		if err := g.do(ctx, "PUT", "/repos/"+g.Repo+"/contents/README.md", body, size, "application/json", nil); err != nil {
			return false, err
		}
	}
	return false, nil
}

// Upload publishes the archive at path as a new release, split into parts
// when it is larger than one asset may be.
func (g *GitHub) Upload(ctx context.Context, path string, m *Manifest) (*Release, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	stamp := m.CreatedAt.UTC().Format("20060102-150405")
	kind, title := "Full backup", "Backup "+m.CreatedAt.UTC().Format("2006-01-02 15:04 UTC")
	if m.Base != "" {
		kind = "Changes since " + m.Base
		title += " (changes)"
	}
	notes := fmt.Sprintf("%s. Lectern %s on %s: %d sessions, %d files.\n\nRestore: `lectern backup restore %s%s`",
		kind, m.Version, m.Host, m.Sessions, m.Files, tagPrefix, stamp)
	if m.Base != "" {
		// Read back by Release.Base; restore fetches the full backup first.
		notes += "\n\nBase: " + m.Base
	}
	if len(m.Skipped) > 0 {
		notes += fmt.Sprintf("\n\n%d items could not be saved; see manifest.json inside the archive.", len(m.Skipped))
	}
	body, size := g.jsonBody(map[string]any{
		"tag_name": tagPrefix + stamp, "name": title, "body": notes,
	})
	var rel Release
	if err := g.do(ctx, "POST", "/repos/"+g.Repo+"/releases", body, size, "application/json", &rel); err != nil {
		return nil, err
	}
	uploads := strings.Replace(g.api(), "://api.", "://uploads.", 1)
	parts := int((st.Size() + partSize - 1) / partSize)
	if parts == 0 {
		parts = 1
	}
	for i := 0; i < parts; i++ {
		name := "lectern-backup-" + stamp + ".tar.gz.age"
		if parts > 1 {
			name += fmt.Sprintf(".part%02d", i+1)
		}
		n := min(int64(partSize), st.Size()-int64(i)*partSize)
		u := fmt.Sprintf("%s/repos/%s/releases/%d/assets?name=%s", uploads, g.Repo, rel.ID, url.QueryEscape(name))
		var a Asset
		if err := g.do(ctx, "POST", u, io.NewSectionReader(f, int64(i)*partSize, n), n, "application/octet-stream", &a); err != nil {
			// Leave no half-uploaded backup behind to be mistaken for a good one.
			g.delete(context.WithoutCancel(ctx), rel)
			return nil, err
		}
		rel.Assets = append(rel.Assets, a)
	}
	return &rel, nil
}

// List returns the backups, newest first.
func (g *GitHub) List(ctx context.Context) ([]Release, error) {
	var out []Release
	for page := 1; page <= 10; page++ {
		var rels []Release
		if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/releases?per_page=100&page=%d", g.Repo, page), nil, -1, "", &rels); err != nil {
			return nil, err
		}
		for _, r := range rels {
			if strings.HasPrefix(r.Tag, tagPrefix) {
				out = append(out, r)
			}
		}
		if len(rels) < 100 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tag > out[j].Tag })
	return out, nil
}

// Prune deletes all but the newest keep backups.
func (g *GitHub) Prune(ctx context.Context, keep int) (int, error) {
	if keep < 1 {
		keep = 1
	}
	rels, err := g.List(ctx)
	if err != nil {
		return 0, err
	}
	// A kept differential needs its full backup, however old.
	needed := map[string]bool{}
	for _, r := range rels[:min(keep, len(rels))] {
		needed[r.Tag] = true
		if b := r.Base(); b != "" {
			needed[b] = true
		}
	}
	n := 0
	for _, r := range rels {
		if needed[r.Tag] {
			continue
		}
		if err := g.delete(ctx, r); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (g *GitHub) delete(ctx context.Context, r Release) error {
	if err := g.do(ctx, "DELETE", fmt.Sprintf("/repos/%s/releases/%d", g.Repo, r.ID), nil, -1, "", nil); err != nil && !isStatus(err, 404) {
		return err
	}
	err := g.do(ctx, "DELETE", "/repos/"+g.Repo+"/git/refs/tags/"+url.PathEscape(r.Tag), nil, -1, "", nil)
	if err != nil && !isStatus(err, 404) && !isStatus(err, 422) {
		return err
	}
	return nil
}

// Find returns the backup with this tag, or the newest for "latest" or "".
func (g *GitHub) Find(ctx context.Context, tag string) (*Release, error) {
	rels, err := g.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rels {
		if tag == "" || tag == "latest" || r.Tag == tag || r.Tag == tagPrefix+tag {
			if len(r.Assets) == 0 {
				continue // an upload that never finished
			}
			return &r, nil
		}
	}
	if tag == "" || tag == "latest" {
		return nil, fmt.Errorf("github: no backups in %s yet", g.Repo)
	}
	return nil, fmt.Errorf("github: no backup %q in %s", tag, g.Repo)
}

// Download writes a backup's parts, in order, to w.
func (g *GitHub) Download(ctx context.Context, r *Release, w io.Writer) error {
	assets := append([]Asset(nil), r.Assets...)
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	for _, a := range assets {
		if err := g.downloadAsset(ctx, a, w); err != nil {
			return err
		}
	}
	return nil
}

func (g *GitHub) downloadAsset(ctx context.Context, a Asset, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := g.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &APIError{Status: resp.StatusCode, Message: "download " + a.Name}
	}
	n, err := io.Copy(w, resp.Body)
	if err == nil && a.Size > 0 && n != a.Size {
		err = fmt.Errorf("github: %s: got %d of %d bytes", a.Name, n, a.Size)
	}
	return err
}

// RestoreRelease downloads a backup and restores it; for a differential
// one, its full backup is restored first and the changes laid over it.
func (g *GitHub) RestoreRelease(ctx context.Context, rel *Release, passphrase string, to Target, tmpDir string, progress func(string)) (*Result, error) {
	layers := []*Release{rel}
	if base := rel.Base(); base != "" {
		full, err := g.Find(ctx, base)
		if err != nil {
			return nil, fmt.Errorf("the full backup %s this one builds on: %w", base, err)
		}
		layers = []*Release{full, rel}
	}
	var res *Result
	placed := map[string]bool{}
	for i, layer := range layers {
		t := to
		t.SkipDB = i < len(layers)-1
		t.Replace = placed
		r, err := g.restoreOne(ctx, layer, passphrase, t, tmpDir, progress)
		if err != nil {
			return nil, err
		}
		for p := range r.Placed {
			placed[p] = true
		}
		if res == nil {
			res = r
			continue
		}
		res.Manifest, res.Database, res.EnvFile, res.Remote = r.Manifest, r.Database, r.EnvFile, r.Remote
		res.Files += r.Files
		res.Kept = r.Kept
		res.Placed = placed
	}
	return res, nil
}

func (g *GitHub) restoreOne(ctx context.Context, rel *Release, passphrase string, to Target, tmpDir string, progress func(string)) (*Result, error) {
	f, err := os.CreateTemp(tmpDir, ".lectern-download-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if progress != nil {
		progress(fmt.Sprintf("Downloading %s (%s) …", rel.Tag, humanSize(rel.Size())))
	}
	if err := g.Download(ctx, rel, f); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return Restore(f, passphrase, to)
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n>>10)
}
