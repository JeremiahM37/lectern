package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/backup"
)

// Backups (internal/backup): Settings → Backups. A backup holds every
// secret this server has, so everything here except reading the status is
// for a person (the same bar as deciding an approval); an agent on this
// machine authenticates as local and is refused.

type backupView struct {
	Repo          string        `json:"repo"`
	Keep          int           `json:"keep"`
	EveryHours    int           `json:"every_hours"`
	HasPassphrase bool          `json:"has_passphrase"`
	GitHub        string        `json:"github"` // where the token comes from; "" = none
	Status        backup.Status `json:"status"`
}

type backupIn struct {
	Repo       *string `json:"repo"`
	Keep       *int    `json:"keep"`
	EveryHours *int    `json:"every_hours"`
	// NewPassphrase generates a passphrase and returns it once.
	NewPassphrase bool `json:"new_passphrase"`
	// Passphrase sets one the operator chose (e.g. restoring onto a new box).
	Passphrase *string `json:"passphrase"`
	// CreateRepo makes the repository (private) if it does not exist yet.
	CreateRepo bool `json:"create_repo"`
}

func (s *Server) backupPerson(w http.ResponseWriter, r *http.Request) bool {
	principal, ok := auth.FromContext(r.Context())
	if !ok || s.Auth == nil || !s.Auth.CanDecide(principal) {
		httpError(w, 403, "backups are for you, not for agents: sign in from your own device")
		return false
	}
	if s.Backups == nil {
		httpError(w, 503, "backups are not available on this server")
		return false
	}
	return true
}

func (s *Server) backupState(ctx context.Context) backupView {
	set := s.Backups.Settings()
	_, source := backup.GitHubToken(ctx)
	return backupView{
		Repo: set.Repo, Keep: set.Keep, EveryHours: set.EveryHours,
		HasPassphrase: set.Passphrase != "", GitHub: source, Status: s.Backups.Status(),
	}
}

func (s *Server) getBackup(w http.ResponseWriter, r *http.Request) {
	if s.Backups == nil {
		httpError(w, 503, "backups are not available on this server")
		return
	}
	writeJSON(w, 200, s.backupState(r.Context()))
}

func (s *Server) putBackup(w http.ResponseWriter, r *http.Request) {
	if !s.backupPerson(w, r) {
		return
	}
	var in backupIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	set := s.Backups.Settings()
	if in.Repo != nil {
		set.Repo = *in.Repo
	}
	if in.Keep != nil {
		set.Keep = *in.Keep
	}
	if in.EveryHours != nil {
		set.EveryHours = *in.EveryHours
	}
	var generated string
	switch {
	case in.NewPassphrase:
		generated = backup.NewPassphrase()
		set.Passphrase = generated
	case in.Passphrase != nil:
		if len(*in.Passphrase) < 12 {
			httpError(w, 422, "use a passphrase of at least 12 characters")
			return
		}
		set.Passphrase = *in.Passphrase
	}
	if err := s.Backups.SaveSettings(set); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	out := map[string]any{"backup": s.backupState(r.Context())}
	if generated != "" {
		out["passphrase"] = generated
	}
	if in.CreateRepo {
		gh, err := s.Backups.Repo(r.Context())
		if err == nil {
			var created bool
			created, err = gh.EnsureRepo(r.Context())
			out["created"] = created
		}
		if err != nil {
			out["repo_error"] = err.Error()
		}
	}
	writeJSON(w, 200, out)
}

// runBackup starts a backup and returns at once; the status says how it went.
func (s *Server) runBackup(w http.ResponseWriter, r *http.Request) {
	if !s.backupPerson(w, r) {
		return
	}
	if st := s.Backups.Status(); st.Running {
		httpError(w, 409, "%s", backup.ErrBusy)
		return
	}
	go s.Backups.Run(context.Background())
	// Let the runner mark itself busy so the reply already shows it.
	for i := 0; i < 20 && !s.Backups.Status().Running; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	writeJSON(w, 202, s.backupState(r.Context()))
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	if !s.backupPerson(w, r) {
		return
	}
	gh, err := s.Backups.Repo(r.Context())
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	rels, err := gh.List(r.Context())
	if err != nil {
		httpError(w, 502, "%s", err)
		return
	}
	type item struct {
		Tag       string    `json:"tag"`
		URL       string    `json:"url"`
		CreatedAt time.Time `json:"created_at"`
		Bytes     int64     `json:"bytes"`
	}
	out := make([]item, 0, len(rels))
	for _, rel := range rels {
		out = append(out, item{rel.Tag, rel.URL, rel.CreatedAt, rel.Size()})
	}
	writeJSON(w, 200, out)
}

// downloadBackup streams a fresh, encrypted backup to the caller.
func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	if !s.backupPerson(w, r) {
		return
	}
	if !s.Backups.Settings().HasPassphrase() {
		httpError(w, 409, "set a backup passphrase first; exports are always encrypted")
		return
	}
	name := fmt.Sprintf("lectern-backup-%s.tar.gz.age", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	cw := &countingWriter{w: w}
	if _, err := s.Backups.Export(r.Context(), cw); err != nil {
		if cw.n == 0 {
			code := 500
			if errors.Is(err, backup.ErrBusy) {
				code = 409
			}
			w.Header().Del("Content-Disposition")
			httpError(w, code, "%s", err)
			return
		}
		// Bytes are already out: cut the response so the partial file fails
		// to decrypt instead of looking whole.
		if s.Log != nil && !strings.Contains(err.Error(), "broken pipe") {
			s.Log.Warn("backup download failed", "err", err)
		}
		panic(http.ErrAbortHandler)
	}
}
