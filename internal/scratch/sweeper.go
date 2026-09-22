package scratch

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/store"
)

// Sweeper applies the sweep across every target that has hosted a session.
type Sweeper struct {
	DB        *store.DB
	Reg       *executor.Registry
	Log       *slog.Logger
	Days      float64 // idle days before an empty directory is removed
	TrashDays float64 // days a removed directory stays recoverable
}

// TargetReport is one target's scratch root, or why it could not be read.
type TargetReport struct {
	TargetID   int64   `json:"target_id"`
	TargetName string  `json:"target_name"`
	Root       string  `json:"root"`
	Entries    []Entry `json:"entries"`
	Error      string  `json:"error,omitempty"`
}

// Result is what a sweep did, or with DryRun what it would do.
type Result struct {
	DryRun  bool           `json:"dry_run"`
	Days    float64        `json:"days"`
	Targets []TargetReport `json:"targets"`
	Trashed []string       `json:"trashed"`
	Purged  int            `json:"purged"`
}

func runner(ex executor.Executor) Runner {
	return func(ctx context.Context, script string) (string, error) {
		r, err := ex.Run(ctx, script, executor.RunOpts{Timeout: 180})
		if err != nil {
			return "", err
		}
		if !r.OK() {
			return r.Stdout, fmt.Errorf("scratch script failed: %s", strings.TrimSpace(r.Stderr))
		}
		return r.Stdout, nil
	}
}

func sessionsOn(rows []*store.ScratchSession, targetID int64) []Session {
	var out []Session
	for _, r := range rows {
		if r.TargetID != targetID {
			continue
		}
		out = append(out, Session{ID: r.ID, Name: r.Name, Agent: r.Agent, Workdir: r.Workdir,
			Project: r.ProjectID != nil, Live: r.EndedAt == nil, LastSeen: r.LastSeen,
			NativeID: r.ResumeID != "" || r.NativeCID != "", CanResume: r.ResumeID != "" || r.NativeCID != ""})
	}
	return out
}

// report inspects one target. It searches agent history only for directories
// that every cheaper signal already calls removable.
func (w *Sweeper) report(ctx context.Context, t *store.Target, rows []*store.ScratchSession) (TargetReport, Runner) {
	rep := TargetReport{TargetID: t.ID, TargetName: t.Name, Entries: []Entry{}}
	ex, err := w.Reg.For(t)
	if err != nil {
		rep.Error = err.Error()
		return rep, nil
	}
	run := runner(ex)
	root, dirs, err := Inspect(ctx, run)
	if err != nil {
		rep.Error = err.Error()
		return rep, nil
	}
	rep.Root = root
	sessions := sessionsOn(rows, t.ID)
	now := store.Now()
	var unsearched []string
	for _, d := range dirs {
		e := Classify(d, sessions, now, w.Days, nil)
		if e.NeedsHistoryCheck {
			unsearched = append(unsearched, d.Path)
		}
		rep.Entries = append(rep.Entries, e)
	}
	if len(unsearched) > 0 {
		hits, err := HistoryMentions(ctx, run, unsearched)
		if err != nil && w.Log != nil {
			w.Log.Warn("scratch: agent history could not be searched; keeping the candidates", "target", t.Name, "err", err)
		}
		for i, e := range rep.Entries {
			if hit, ok := hits[e.Path]; ok {
				rep.Entries[i] = Classify(e.Dir, sessions, now, w.Days, &hit)
			}
		}
	}
	for i := range rep.Entries {
		rep.Entries[i].TargetID, rep.Entries[i].TargetName = t.ID, t.Name
		if rep.Entries[i].Sessions == nil {
			rep.Entries[i].Sessions = []Session{}
		}
	}
	return rep, run
}

// Sweep inspects every target that has hosted a session and, unless dryRun,
// trashes what is empty and old and purges what has sat in the trash too long.
// onlyTarget narrows it to one target; zero means all.
func (w *Sweeper) Sweep(ctx context.Context, dryRun bool, onlyTarget int64) (*Result, error) {
	res := &Result{DryRun: dryRun, Days: w.Days, Targets: []TargetReport{}, Trashed: []string{}}
	rows, err := w.DB.ScratchSessions()
	if err != nil {
		return nil, err
	}
	hosted := map[int64]bool{}
	for _, r := range rows {
		hosted[r.TargetID] = true
	}
	targets, err := w.DB.Targets()
	if err != nil {
		return nil, err
	}
	for _, t := range targets {
		if !hosted[t.ID] || (onlyTarget != 0 && t.ID != onlyTarget) {
			continue
		}
		rep, run := w.report(ctx, t, rows)
		res.Targets = append(res.Targets, rep)
		if dryRun || run == nil {
			continue
		}
		var names []string
		for _, e := range rep.Entries {
			if e.Verdict == Empty {
				names = append(names, e.Name)
			}
		}
		// A session can be restored into its old directory at any moment. Read the
		// rows again immediately before moving anything, and drop whatever is
		// live now even if it was not a moment ago.
		if len(names) > 0 {
			if fresh, err := w.DB.ScratchSessions(); err == nil {
				names = stillUnclaimed(names, rep.Root, sessionsOn(fresh, t.ID))
			} else {
				names = nil
			}
		}
		if len(names) > 0 {
			moved, err := Trash(ctx, run, names, int64(store.Now()))
			if err != nil && w.Log != nil {
				w.Log.Warn("scratch: trash failed", "target", t.Name, "err", err)
			}
			for _, name := range moved {
				res.Trashed = append(res.Trashed, t.Name+":"+name)
			}
		}
		if n, err := Purge(ctx, run, w.TrashDays, int64(store.Now())); err == nil {
			res.Purged += n
		}
	}
	// Named one by one: a directory that goes missing should be findable in the
	// log by its own name, not inferred from a count.
	for _, name := range res.Trashed {
		if w.Log != nil {
			w.Log.Info("scratch: moved an idle empty workspace to the trash", "workspace", name,
				"idle_days", w.Days, "recoverable_days", w.TrashDays)
		}
	}
	return res, nil
}

func stillUnclaimed(names []string, root string, sessions []Session) []string {
	var out []string
	for _, name := range names {
		path := strings.TrimRight(root, "/") + "/" + name
		claimed := false
		for _, s := range sessions {
			if (s.Live || s.Project) && under(s.Workdir, path) {
				claimed = true
			}
		}
		if !claimed {
			out = append(out, name)
		}
	}
	return out
}

// find locates one directory for an operator's decision about it.
func (w *Sweeper) find(ctx context.Context, targetID int64, name string) (Entry, Runner, error) {
	if !SafeName(name) {
		return Entry{}, nil, fmt.Errorf("not a scratch directory name")
	}
	t, err := w.DB.Target(targetID)
	if err != nil {
		return Entry{}, nil, err
	}
	rows, err := w.DB.ScratchSessions()
	if err != nil {
		return Entry{}, nil, err
	}
	rep, run := w.report(ctx, t, rows)
	if run == nil {
		return Entry{}, nil, fmt.Errorf("%s", rep.Error)
	}
	for _, e := range rep.Entries {
		if e.Name == name {
			return e, run, nil
		}
	}
	return Entry{}, nil, store.ErrNotFound
}

// Discard trashes a directory a person has decided against. It is recoverable
// until the purge, and refused outright while a session or a project owns it.
func (w *Sweeper) Discard(ctx context.Context, targetID int64, name string) error {
	e, run, err := w.find(ctx, targetID, name)
	if err != nil {
		return err
	}
	if e.Verdict == Live || e.Verdict == Project {
		return fmt.Errorf("refused: %s", strings.Join(e.Reasons, "; "))
	}
	moved, err := Trash(ctx, run, []string{name}, int64(store.Now()))
	if err != nil {
		return err
	}
	if len(moved) != 1 {
		return fmt.Errorf("the directory could not be moved to the trash")
	}
	return nil
}

// Keep exempts a directory from the sweep for good.
func (w *Sweeper) Keep(ctx context.Context, targetID int64, name string) error {
	_, run, err := w.find(ctx, targetID, name)
	if err != nil {
		return err
	}
	return MarkKeep(ctx, run, name)
}
