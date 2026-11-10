package backup

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Setting keys in the server's settings table.
const (
	settingsKey = "backup"
	statusKey   = "backup_status"
)

// Settings is the operator's backup configuration. LECTERN_BACKUP_REPO and
// LECTERN_BACKUP_PASSPHRASE, when set, win over what is stored.
type Settings struct {
	Repo       string `json:"repo"`
	Keep       int    `json:"keep"`
	EveryHours int    `json:"every_hours"` // 0 = only when asked
	// FullEveryDays: a full backup this often; the ones between hold only
	// the files changed since it (transcripts run to gigabytes).
	FullEveryDays int    `json:"full_every_days"`
	Passphrase    string `json:"passphrase,omitempty"`
}

// HasPassphrase reports whether backups can be encrypted.
func (s Settings) HasPassphrase() bool { return s.Passphrase != "" }

// Status is the outcome of the last backup.
type Status struct {
	Running   bool      `json:"running"`
	StartedAt time.Time `json:"started_at,omitzero"`
	LastAt    time.Time `json:"last_at,omitzero"`
	LastOKAt  time.Time `json:"last_ok_at,omitzero"`
	Error     string    `json:"error,omitempty"`
	Tag       string    `json:"tag,omitempty"`
	URL       string    `json:"url,omitempty"`
	Bytes     int64     `json:"bytes,omitempty"`
	Sessions  int       `json:"sessions,omitempty"`
	Files     int       `json:"files,omitempty"`
	Skipped   []string  `json:"skipped,omitempty"`
	// Full is the last full backup, which the next ones build on.
	Full   string    `json:"full,omitempty"`
	FullAt time.Time `json:"full_at,omitzero"`
}

// Store is where settings and status are kept (store.DB).
type Store interface {
	Setting(key string) string
	SetSetting(key, value string) error
}

// Runner makes backups for a server: on demand and on a schedule.
type Runner struct {
	Store   Store
	Source  func(context.Context) (Source, error)
	TempDir string
	Log     *slog.Logger
	// GitHub returns the client for a repository; nil uses GitHubToken.
	GitHub func(ctx context.Context, repo string) (*GitHub, error)

	mu      sync.Mutex
	running bool
}

// Settings returns the effective configuration.
func (r *Runner) Settings() Settings {
	s := Settings{EveryHours: 24} // daily until the operator says otherwise
	json.Unmarshal([]byte(r.Store.Setting(settingsKey)), &s)
	if v := strings.TrimSpace(os.Getenv("LECTERN_BACKUP_REPO")); v != "" {
		s.Repo = v
	}
	if v := os.Getenv("LECTERN_BACKUP_PASSPHRASE"); v != "" {
		s.Passphrase = v
	}
	if v, err := strconv.Atoi(os.Getenv("LECTERN_BACKUP_EVERY_HOURS")); err == nil {
		s.EveryHours = v
	}
	if s.Keep <= 0 {
		s.Keep = 14
	}
	if s.FullEveryDays <= 0 {
		s.FullEveryDays = 7
	}
	return s
}

// SaveSettings stores the configuration as given.
func (r *Runner) SaveSettings(s Settings) error {
	s.Repo = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s.Repo), "https://github.com/"), ".git"))
	if s.Keep < 0 || s.EveryHours < 0 {
		return errors.New("keep and every_hours cannot be negative")
	}
	raw, _ := json.Marshal(s)
	return r.Store.SetSetting(settingsKey, string(raw))
}

// NewPassphrase returns 30 random characters (150 bits) in groups of five:
// far past guessing, and short enough to write down.
func NewPassphrase() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	var b strings.Builder
	for i := 0; i < 30; i++ {
		if i > 0 && i%5 == 0 {
			b.WriteByte('-')
		}
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String()
}

// Status returns the last backup's outcome.
func (r *Runner) Status() Status {
	var s Status
	json.Unmarshal([]byte(r.Store.Setting(statusKey)), &s)
	r.mu.Lock()
	s.Running = r.running
	r.mu.Unlock()
	return s
}

func (r *Runner) saveStatus(s Status) {
	s.Running = false
	raw, _ := json.Marshal(s)
	if err := r.Store.SetSetting(statusKey, string(raw)); err != nil && r.Log != nil {
		r.Log.Warn("backup status not saved", "err", err)
	}
}

// ErrBusy means a backup is already being made.
var ErrBusy = errors.New("a backup is already running")

func (r *Runner) begin() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return ErrBusy
	}
	r.running = true
	return nil
}

func (r *Runner) end() {
	r.mu.Lock()
	r.running = false
	r.mu.Unlock()
}

func (r *Runner) github(ctx context.Context, repo string) (*GitHub, error) {
	if r.GitHub != nil {
		return r.GitHub(ctx, repo)
	}
	token, _ := GitHubToken(ctx)
	if token == "" {
		return nil, errors.New("no GitHub login on the server: run `gh auth login` there, or set LECTERN_BACKUP_GITHUB_TOKEN")
	}
	return &GitHub{Repo: repo, Token: token}, nil
}

// Repo returns the configured repository's client.
func (r *Runner) Repo(ctx context.Context) (*GitHub, error) {
	s := r.Settings()
	if s.Repo == "" {
		return nil, errors.New("no backup repository set")
	}
	return r.github(ctx, s.Repo)
}

// Run makes a backup and uploads it to the repository, then deletes the
// oldest beyond Keep. Backups leave this machine only encrypted.
func (r *Runner) Run(ctx context.Context) (Status, error) {
	if err := r.begin(); err != nil {
		return r.Status(), err
	}
	defer r.end()
	st := r.Status()
	st.StartedAt = time.Now().UTC()
	err := r.run(ctx, &st)
	st.LastAt = time.Now().UTC()
	if err != nil {
		st.Error = err.Error()
		if r.Log != nil {
			r.Log.Warn("backup failed", "err", err)
		}
	} else {
		st.Error = ""
		st.LastOKAt = st.LastAt
		if r.Log != nil {
			r.Log.Info("backup uploaded", "tag", st.Tag, "bytes", st.Bytes, "sessions", st.Sessions, "skipped", len(st.Skipped))
		}
	}
	st.Running = false
	r.saveStatus(st)
	return st, err
}

func (r *Runner) run(ctx context.Context, st *Status) error {
	s := r.Settings()
	if s.Repo == "" {
		return errors.New("no backup repository set")
	}
	if s.Passphrase == "" {
		return errors.New("no backup passphrase set; backups are only uploaded encrypted")
	}
	gh, err := r.github(ctx, s.Repo)
	if err != nil {
		return err
	}
	if _, err := gh.EnsureRepo(ctx); err != nil {
		return err
	}
	rels, err := gh.List(ctx)
	if err != nil {
		return err
	}
	base, since := st.Full, st.FullAt
	found := false
	for _, rel := range rels {
		found = found || (rel.Tag == base && len(rel.Assets) > 0)
	}
	if !found || time.Since(since) >= time.Duration(s.FullEveryDays)*24*time.Hour {
		base, since = "", time.Time{}
	}
	f, err := os.CreateTemp(r.TempDir, ".lectern-backup-*.tar.gz.age")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	m, err := r.write(ctx, f, s.Passphrase, base, since)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Prove it opens before it counts as a backup.
	check, err := os.Open(f.Name())
	if err != nil {
		return err
	}
	_, err = Verify(check, s.Passphrase)
	check.Close()
	if err != nil {
		return fmt.Errorf("the new backup did not verify: %w", err)
	}
	rel, err := gh.Upload(ctx, f.Name(), m)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	st.Tag, st.URL, st.Bytes = rel.Tag, rel.URL, rel.Size()
	st.Sessions, st.Files, st.Skipped = m.Sessions, m.Files, m.Skipped
	if base == "" {
		st.Full, st.FullAt = rel.Tag, m.CreatedAt
	}
	if _, err := gh.Prune(ctx, s.Keep); err != nil && r.Log != nil {
		r.Log.Warn("old backups not pruned", "err", err)
	}
	return nil
}

func (r *Runner) write(ctx context.Context, w io.Writer, passphrase, base string, since time.Time) (*Manifest, error) {
	src, err := r.Source(ctx)
	if err != nil {
		return nil, err
	}
	if src.TempDir == "" {
		src.TempDir = r.TempDir
	}
	src.Base, src.Since = base, since
	return Write(ctx, w, src, passphrase)
}

// Export writes an encrypted backup straight to w (a download).
func (r *Runner) Export(ctx context.Context, w io.Writer) (*Manifest, error) {
	s := r.Settings()
	if s.Passphrase == "" {
		return nil, errors.New("set a backup passphrase first; exports are always encrypted")
	}
	if err := r.begin(); err != nil {
		return nil, err
	}
	defer r.end()
	return r.write(ctx, w, s.Passphrase, "", time.Time{})
}

// Due reports whether the schedule wants a backup now.
func (r *Runner) Due(now time.Time) bool {
	s := r.Settings()
	if s.EveryHours <= 0 || s.Repo == "" || s.Passphrase == "" {
		return false
	}
	st := r.Status()
	// A failed attempt waits an hour, not a whole interval, before retrying.
	if st.LastAt.After(st.LastOKAt) && now.Sub(st.LastAt) < time.Hour {
		return false
	}
	return now.Sub(st.LastOKAt) >= time.Duration(s.EveryHours)*time.Hour
}

// Loop makes scheduled backups until ctx ends.
func (r *Runner) Loop(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if r.Due(time.Now()) {
				r.Run(ctx)
			}
		}
	}
}
