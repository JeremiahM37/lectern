package effects

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Status is the outcome of probing one claim.
type Status string

const (
	// StatusVerified means the probe observed the claimed effect.
	StatusVerified Status = "verified"
	// StatusContradicted means the probe observed the opposite.
	StatusContradicted Status = "contradicted"
	// StatusUnknown means the probe could not decide: no evidence either way.
	StatusUnknown Status = "unknown"
)

// Verdict is what Probe concluded and the observation behind it.
type Verdict struct {
	Status   Status
	Evidence string
}

// Facts is what the probes may compare against: when the turn began, the most
// recent test run in the session, and the fingerprint of the tree now.
type Facts struct {
	Since              time.Time
	LastCheck          *store.SessionCheck
	CurrentFingerprint string
}

// probeTimeout bounds each read-only command. A probe that hangs must come
// back as unknown, not stall the turn.
const probeTimeout = 15

// Probe checks one claim against the machine behind ex. dir is the project
// directory the agent worked in. Every command it runs is read-only.
func Probe(ctx context.Context, ex executor.Executor, dir string, c Claim, f Facts) Verdict {
	switch c.Kind {
	case KindPushed:
		return probePushed(ctx, ex, dir)
	case KindCommitted:
		return probeCommitted(ctx, ex, dir)
	case KindTestsPass:
		return probeTests(f)
	case KindRestarted:
		return probeRestarted(ctx, ex, dir, c.Target, f.Since)
	case KindURLOK:
		return probeURL(ctx, ex, dir, c.Target)
	case KindPortListening:
		return probePort(ctx, ex, dir, c.Target)
	case KindFileExists:
		return probeFile(ctx, ex, dir, c.Target)
	}
	return Verdict{Status: StatusUnknown, Evidence: "no probe for claim kind " + string(c.Kind)}
}

// run executes one read-only command with the probe timeout.
func run(ctx context.Context, ex executor.Executor, dir, cmd string) (executor.Result, error) {
	return ex.Run(ctx, cmd, executor.RunOpts{Cwd: dir, Timeout: probeTimeout})
}

// output runs cmd and returns its trimmed stdout. A non-zero exit or a failure
// to reach the target is an error that carries the last line of stderr.
func output(ctx context.Context, ex executor.Executor, dir, cmd string) (string, error) {
	res, err := run(ctx, ex, dir, cmd)
	if err != nil {
		return "", err
	}
	if res.RC != 0 {
		return "", fmt.Errorf("exit %d: %s", res.RC, lastLine(res.Stderr, res.Stdout))
	}
	return strings.TrimSpace(res.Stdout), nil
}

func unknown(format string, args ...any) Verdict {
	return Verdict{Status: StatusUnknown, Evidence: fmt.Sprintf(format, args...)}
}

func verified(format string, args ...any) Verdict {
	return Verdict{Status: StatusVerified, Evidence: fmt.Sprintf(format, args...)}
}

func contradicted(format string, args ...any) Verdict {
	return Verdict{Status: StatusContradicted, Evidence: fmt.Sprintf(format, args...)}
}

// lastLine returns the last non-empty line of the first non-empty text.
func lastLine(texts ...string) string {
	for _, t := range texts {
		lines := strings.Split(strings.TrimSpace(t), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			return last
		}
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// probePushed compares HEAD with the remote branch that the current branch
// tracks. HEAD on the remote, or behind it, is verified. Anything else is
// contradicted, because the commit the agent is talking about is not on the
// remote.
func probePushed(ctx context.Context, ex executor.Executor, dir string) Verdict {
	head, err := output(ctx, ex, dir, "git rev-parse HEAD")
	if err != nil {
		return unknown("cannot read HEAD: %v", err)
	}
	upstream, err := output(ctx, ex, dir, "git rev-parse --abbrev-ref --symbolic-full-name "+executor.ShellQuote("@{u}"))
	if err != nil {
		return unknown("no upstream branch to compare HEAD %s with", short(head))
	}
	remote, branch, ok := strings.Cut(upstream, "/")
	if !ok || branch == "" {
		return unknown("upstream %q is not remote/branch", upstream)
	}
	ls, err := output(ctx, ex, dir, "git ls-remote "+executor.ShellQuote(remote)+" "+executor.ShellQuote("refs/heads/"+branch))
	if err != nil {
		return unknown("cannot reach %s: %v", remote, err)
	}
	remoteSHA := ""
	if fields := strings.Fields(ls); len(fields) > 0 {
		remoteSHA = fields[0]
	}
	if remoteSHA == "" {
		return contradicted("HEAD %s is not on %s (the remote has no %s branch)", short(head), upstream, branch)
	}
	if remoteSHA == head {
		return verified("HEAD %s is on %s", short(head), upstream)
	}
	// Ancestry needs the remote commit in the local object store. Without it
	// we cannot tell "behind" from "diverged", so the honest answer is unknown.
	if _, err := run(ctx, ex, dir, "git cat-file -e "+executor.ShellQuote(remoteSHA+"^{commit}")); err != nil {
		return unknown("remote %s is at %s, which is not in the local clone", upstream, short(remoteSHA))
	}
	res, err := run(ctx, ex, dir, "git merge-base --is-ancestor HEAD "+executor.ShellQuote(remoteSHA))
	if err != nil {
		return unknown("ancestry check failed: %v", err)
	}
	switch res.RC {
	case 0:
		return verified("HEAD %s is an ancestor of %s (remote at %s)", short(head), upstream, short(remoteSHA))
	case 1:
		return contradicted("HEAD %s is not on %s (remote at %s)", short(head), upstream, short(remoteSHA))
	}
	return unknown("ancestry check exited %d: %s", res.RC, lastLine(res.Stderr))
}

// probeCommitted reports uncommitted changes to tracked files. Untracked files
// are ignored: a new file the agent never added does not undo a commit.
func probeCommitted(ctx context.Context, ex executor.Executor, dir string) Verdict {
	out, err := output(ctx, ex, dir, "git status --porcelain --untracked-files=no")
	if err != nil {
		return unknown("git status failed: %v", err)
	}
	n := countLines(out)
	if n == 0 {
		return verified("no uncommitted changes to tracked files")
	}
	if n == 1 {
		return contradicted("1 tracked file has uncommitted changes")
	}
	return contradicted("%d tracked files have uncommitted changes", n)
}

func countLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// probeTests trusts only the session's own recorded test run. A run on the
// current tree that passed is verified. A failed run is contradicted whatever
// the tree looks like now, because the claim cannot hold while a run fails.
func probeTests(f Facts) Verdict {
	lc := f.LastCheck
	if lc == nil {
		return unknown("no test run is recorded for this session")
	}
	switch lc.Status {
	case "failed":
		return contradicted("%s failed: %s", lc.Command, lastLine(lc.OutputTail))
	case "passed":
		if lc.Fingerprint != "" && lc.Fingerprint == f.CurrentFingerprint {
			return verified("%s passed on the current tree", lc.Command)
		}
		return unknown("%s passed, but the tree changed after it", lc.Command)
	}
	return unknown("last test run %q is %s", lc.Command, lc.Status)
}

// probeURL requests the URL and reports the HTTP status. Only 2xx and 3xx count
// as live. TLS verification is skipped for https because the homelab serves
// self-signed certificates on the LAN; this is still a reachability check.
func probeURL(ctx context.Context, ex executor.Executor, dir, url string) Verdict {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return unknown("%q is not an http(s) URL", url)
	}
	insecure := ""
	if strings.HasPrefix(url, "https://") {
		insecure = "-k "
	}
	cmd := "curl -sS -o /dev/null -m 10 -w '%{http_code}' " + insecure + executor.ShellQuote(url)
	res, err := run(ctx, ex, dir, cmd)
	if err != nil {
		return unknown("cannot run curl: %v", err)
	}
	code := strings.TrimSpace(res.Stdout)
	if res.RC != 0 {
		return contradicted("%s: curl: %s", url, lastLine(res.Stderr))
	}
	if len(code) == 3 && (code[0] == '2' || code[0] == '3') {
		return verified("HTTP %s from %s", code, url)
	}
	return contradicted("HTTP %s from %s", code, url)
}

// probePort asks the kernel for a listening TCP socket on the port.
func probePort(ctx context.Context, ex executor.Executor, dir, port string) Verdict {
	if !validPort(port) {
		return unknown("%q is not a port", port)
	}
	res, err := run(ctx, ex, dir, "ss -ltnH "+executor.ShellQuote("sport = :"+port))
	if err != nil {
		return unknown("cannot run ss: %v", err)
	}
	if res.RC != 0 {
		return unknown("ss exited %d: %s", res.RC, lastLine(res.Stderr))
	}
	if strings.TrimSpace(res.Stdout) == "" {
		return contradicted("nothing is listening on :%s", port)
	}
	return verified("a TCP socket is listening on :%s", port)
}

// probeFile checks that the path exists. Relative paths resolve against dir,
// and "~/" paths against the target's home directory.
func probeFile(ctx context.Context, ex executor.Executor, dir, path string) Verdict {
	arg := executor.ShellQuote(path)
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		arg = `"$HOME"/` + executor.ShellQuote(rest)
	}
	res, err := run(ctx, ex, dir, "test -e "+arg)
	if err != nil {
		return unknown("cannot run test: %v", err)
	}
	switch res.RC {
	case 0:
		return verified("%s exists", path)
	case 1:
		return contradicted("%s does not exist", path)
	}
	return unknown("test exited %d for %s", res.RC, path)
}
