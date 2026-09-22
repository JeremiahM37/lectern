// Package scratch reclaims throwaway workspaces nobody is using.
//
// Every blank shell and every project-less session gets its own directory under
// the target's scratch root, and nothing ever removed one. Most are empty the
// day they are made and stay that way; a few hold work somebody meant to file
// under a project and never did. The sweep tells those apart, moves the empty
// ones to a trash it empties later, and leaves anything that looks like work for
// a person to decide about.
//
// It fails closed: a directory is removed only when every signal could be read
// and all of them say nothing happened there.
package scratch

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Verdict is what the sweep concluded about one directory.
type Verdict string

const (
	Live    Verdict = "live"    // a session is running in it
	Project Verdict = "project" // a session there belongs to a project
	Kept    Verdict = "kept"    // a person marked it to keep
	Work    Verdict = "work"    // something happened there; a person decides
	Recent  Verdict = "recent"  // empty, but not yet old enough to remove
	Empty   Verdict = "empty"   // empty and old: the sweep removes it
)

// KeepMarker is the file that exempts a directory from the sweep for good.
const KeepMarker = ".lectern-keep"

// Dir is what the target reported about one scratch directory.
type Dir struct {
	Name        string  `json:"name"`
	Path        string  `json:"path"`
	Mtime       float64 `json:"mtime"`
	Files       int     `json:"files"`
	Commits     int     `json:"commits"`
	ClaudeBytes int64   `json:"claude_bytes"`
	SizeKB      int64   `json:"size_kb"`
	Keep        bool    `json:"keep"`
	// Unreadable is set when any signal could not be measured. Such a directory
	// is treated as holding work, whatever the other signals say.
	Unreadable bool `json:"unreadable,omitempty"`
}

// Session is the part of a session row the decision needs.
type Session struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Agent     string  `json:"agent"`
	Workdir   string  `json:"-"`
	Project   bool    `json:"-"`
	Live      bool    `json:"-"`
	LastSeen  float64 `json:"-"`
	NativeID  bool    `json:"-"` // the agent recorded a conversation identity
	CanResume bool    `json:"can_resume"`
}

// Entry is one directory with its verdict and the evidence for it.
type Entry struct {
	Dir
	TargetID   int64     `json:"target_id"`
	TargetName string    `json:"target_name"`
	Sessions   []Session `json:"sessions"`
	AgeDays    float64   `json:"age_days"`
	Verdict    Verdict   `json:"verdict"`
	Reasons    []string  `json:"reasons"`
	// NeedsHistoryCheck marks an otherwise-removable directory whose agent
	// history has not been searched yet. That search reads every recorded
	// conversation, so it is only run for directories that pass everything else.
	NeedsHistoryCheck bool `json:"-"`
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// SafeName reports whether a directory name may be handed to a shell command.
// Scratch names come from mktemp over a slug, so anything else is not ours.
func SafeName(name string) bool { return safeName.MatchString(name) && !strings.Contains(name, "..") }

// under reports whether workdir is dir or somewhere inside it.
func under(workdir, dir string) bool {
	return workdir == dir || strings.HasPrefix(workdir, strings.TrimRight(dir, "/")+"/")
}

// Classify decides one directory from what the target and the database say.
// historyHit is whether an agent's recorded conversations mention the path; pass
// nil when that has not been checked.
func Classify(d Dir, sessions []Session, now float64, days float64, historyHit *bool) Entry {
	e := Entry{Dir: d}
	last := d.Mtime
	for _, s := range sessions {
		if !under(s.Workdir, d.Path) {
			continue
		}
		e.Sessions = append(e.Sessions, s)
		if s.LastSeen > last {
			last = s.LastSeen
		}
	}
	e.AgeDays = (now - last) / 86400
	for _, s := range e.Sessions {
		if s.Live {
			e.Verdict, e.Reasons = Live, []string{fmt.Sprintf("session %d is running here", s.ID)}
			return e
		}
	}
	for _, s := range e.Sessions {
		if s.Project {
			e.Verdict, e.Reasons = Project, []string{fmt.Sprintf("session %d belongs to a project", s.ID)}
			return e
		}
	}
	if d.Keep {
		e.Verdict, e.Reasons = Kept, []string{"marked to keep"}
		return e
	}
	var work []string
	if d.Unreadable {
		work = append(work, "could not be inspected")
	}
	if d.Files > 0 {
		work = append(work, plural(d.Files, "file"))
	}
	if d.Commits > 0 {
		work = append(work, plural(d.Commits, "commit"))
	}
	if d.ClaudeBytes > 0 {
		work = append(work, fmt.Sprintf("%d KiB of Claude conversation", (d.ClaudeBytes+1023)/1024))
	}
	for _, s := range e.Sessions {
		if s.NativeID {
			work = append(work, fmt.Sprintf("session %d recorded a conversation", s.ID))
			break
		}
	}
	if historyHit != nil && *historyHit {
		work = append(work, "an agent's history mentions it")
	}
	if len(work) > 0 {
		e.Verdict, e.Reasons = Work, work
		return e
	}
	if e.AgeDays < days {
		e.Verdict = Recent
		e.Reasons = []string{fmt.Sprintf("empty; removed after %g days idle", days)}
		return e
	}
	if historyHit == nil {
		e.NeedsHistoryCheck = true
	}
	e.Verdict, e.Reasons = Empty, []string{"no files, commits or conversation"}
	return e
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// ---- what runs on the target -------------------------------------------------

// Runner executes a shell script on one target and returns its stdout.
type Runner func(ctx context.Context, script string) (string, error)

// RootExpr resolves the scratch root on the target. A target that was set up
// under the old name still has its workspaces in ~/agentdeck-scratch, and a
// sweep that looked elsewhere would silently stop covering them; so the old
// directory is used until a new one exists.
const RootExpr = `root="${LECTERN_SCRATCH_ROOT:-${AGENTDECK_SCRATCH_ROOT:-}}"; [ -n "$root" ] || { if [ -d "$HOME/lectern-scratch" ] || [ ! -d "$HOME/agentdeck-scratch" ]; then root="$HOME/lectern-scratch"; else root="$HOME/agentdeck-scratch"; fi; }`

// inspectScript reports every directory directly under the scratch root. A
// field it cannot measure is printed as "?", which Classify reads as work.
const inspectScript = `# lectern-scratch-inspect
` + RootExpr + `
[ -d "$root" ] || exit 0
cd "$root" || exit 0
real=$(pwd -P)
printf 'ROOT\t%s\n' "$real"
claude="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/projects"
for d in *; do
  [ -d "$d" ] && [ ! -L "$d" ] || continue
  case "$d" in *[!A-Za-z0-9._-]*|.*) continue;; esac
  files=$(find "$d" -path "$d/.git" -prune -o -type f ! -name .lectern-keep -print 2>/dev/null | head -n 500 | wc -l) || files='?'
  commits=0
  if [ -e "$d/.git" ]; then
    commits=$(git --git-dir="$d/.git" rev-list --count --all 2>/dev/null) || commits='?'
  fi
  mtime=$(find "$d" -path "$d/.git" -prune -o -printf '%T@\n' 2>/dev/null | sort -n | tail -n 1) || mtime='?'
  slug=$(printf %s "$real/$d" | sed 's/[^A-Za-z0-9]/-/g')
  chat=0
  if [ -d "$claude/$slug" ]; then
    chat=$(cat "$claude/$slug"/*.jsonl 2>/dev/null | wc -c) || chat='?'
  fi
  keep=0; [ -e "$d/.lectern-keep" ] && keep=1
  size=$(du -sk "$d" 2>/dev/null | cut -f1) || size='?'
  printf 'DIR\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$d" "${mtime:-?}" "${files:-?}" "${commits:-?}" "${chat:-?}" "$keep" "${size:-?}"
done
`

// Inspect asks the target what is in its scratch root.
func Inspect(ctx context.Context, run Runner) (root string, dirs []Dir, err error) {
	out, err := run(ctx, inspectScript)
	if err != nil {
		return "", nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		switch {
		case len(f) == 2 && f[0] == "ROOT":
			root = strings.TrimSpace(f[1])
		case len(f) == 8 && f[0] == "DIR" && root != "" && SafeName(f[1]):
			d := Dir{Name: f[1], Path: strings.TrimRight(root, "/") + "/" + f[1], Keep: f[6] == "1"}
			mtime, e1 := strconv.ParseFloat(f[2], 64)
			files, e2 := strconv.Atoi(strings.TrimSpace(f[3]))
			commits, e3 := strconv.Atoi(strings.TrimSpace(f[4]))
			chat, e4 := strconv.ParseInt(strings.TrimSpace(f[5]), 10, 64)
			size, e5 := strconv.ParseInt(strings.TrimSpace(f[7]), 10, 64)
			d.Mtime, d.Files, d.Commits, d.ClaudeBytes, d.SizeKB = mtime, files, commits, chat, size
			d.Unreadable = e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil
			dirs = append(dirs, d)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	return root, dirs, nil
}

// HistoryMentions searches Codex's recorded conversations for each path. Claude
// files its history by directory, which Inspect already measured; Codex files
// by date, so finding a directory means reading them. A search that errors or
// times out reports a hit: not knowing is not the same as nothing being there.
func HistoryMentions(ctx context.Context, run Runner, paths []string) (map[string]bool, error) {
	hits := map[string]bool{}
	if len(paths) == 0 {
		return hits, nil
	}
	var b strings.Builder
	b.WriteString("# lectern-scratch-history\n")
	b.WriteString(`codex="${CODEX_HOME:-$HOME/.codex}/sessions"` + "\n")
	for i, p := range paths {
		hits[p] = true // until the target says otherwise
		fmt.Fprintf(&b, "p=%s\n", shellQuote(p))
		fmt.Fprintf(&b, `if [ ! -d "$codex" ]; then echo "MISS %d"; else `+
			`timeout 60 grep -rlF --include='*.jsonl' -m1 -- "\"$p\"" "$codex" >/dev/null 2>&1; `+
			`case $? in 0) echo "HIT %d";; 1) echo "MISS %d";; *) echo "UNKNOWN %d";; esac; fi`+"\n", i, i, i, i)
	}
	out, err := run(ctx, b.String())
	if err != nil {
		return hits, err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || f[0] != "MISS" {
			continue
		}
		if i, err := strconv.Atoi(f[1]); err == nil && i >= 0 && i < len(paths) {
			hits[paths[i]] = false
		}
	}
	return hits, nil
}

// trashStamp separates a trashed directory's name from the time it was trashed.
// A bare numeric suffix is not enough to call an entry ours: a person's own
// "notes.2026" would read as a directory trashed in 1970.
const trashStamp = ".lec-trashed-"

// Trash moves directories out of the scratch root into the trash, stamped with
// the time so Purge knows how long each has been there. It returns the names
// that actually moved.
func Trash(ctx context.Context, run Runner, names []string, now int64) ([]string, error) {
	var b strings.Builder
	b.WriteString("# lectern-scratch-trash\n" + RootExpr + "\n")
	b.WriteString(`cd "$root" || exit 1` + "\n")
	b.WriteString(`trash="${LECTERN_SCRATCH_TRASH:-$root/.trash}"; mkdir -p "$trash" || exit 1` + "\n")
	for _, name := range names {
		if !SafeName(name) {
			continue
		}
		q := shellQuote(name)
		fmt.Fprintf(&b, `[ -d %s ] && [ ! -L %s ] && mv -- %s "$trash"/%s && echo %s || true`+"\n",
			q, q, q, shellQuote(fmt.Sprintf("%s%s%d", name, trashStamp, now)), shellQuote("TRASHED "+name))
	}
	out, err := run(ctx, b.String())
	if err != nil {
		return nil, err
	}
	var moved []string
	for _, line := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "TRASHED "); ok {
			moved = append(moved, name)
		}
	}
	return moved, nil
}

// Purge deletes trash entries older than days. Only names this package stamped
// are eligible, so nothing else that finds its way into the trash is removed.
func Purge(ctx context.Context, run Runner, days float64, now int64) (int, error) {
	script := "# lectern-scratch-purge\n" + RootExpr + "\n" +
		`trash="${LECTERN_SCRATCH_TRASH:-$root/.trash}"; [ -d "$trash" ] || exit 0; cd "$trash" || exit 0` + "\n" +
		fmt.Sprintf("now=%d; ttl=%d\n", now, int64(days*86400)) +
		`for e in *` + trashStamp + `*; do
  [ -d "$e" ] && [ ! -L "$e" ] || continue
  ts=${e##*` + trashStamp + `}
  case "$ts" in ''|*[!0-9]*) continue;; esac
  [ $((now - ts)) -gt "$ttl" ] || continue
  rm -rf -- "./$e" && echo PURGED
done
`
	out, err := run(ctx, script)
	if err != nil {
		return 0, err
	}
	return strings.Count(out, "PURGED"), nil
}

// MarkKeep exempts a directory from the sweep for good.
func MarkKeep(ctx context.Context, run Runner, name string) error {
	if !SafeName(name) {
		return fmt.Errorf("not a scratch directory name")
	}
	q := shellQuote(name)
	out, err := run(ctx, "# lectern-scratch-keep\n"+RootExpr+"\n"+
		fmt.Sprintf(`cd "$root" && [ -d %s ] && [ ! -L %s ] && : > %s/%s && echo KEPT || true`, q, q, q, KeepMarker))
	if err != nil {
		return err
	}
	if !strings.Contains(out, "KEPT") {
		return fmt.Errorf("no such scratch directory")
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
