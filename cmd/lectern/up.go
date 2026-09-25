package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/cmd/lectern/localruntime"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/console"
	"github.com/JeremiahM37/lectern/v2/internal/onboard"
)

const upHelp = `lectern up — the one-command path to a working local install.

Starts (or reuses) the private local runtime on 127.0.0.1, registers a local
target if one isn't there yet, offers to register the current directory as a
project if it looks like a git repository, opens it in your browser, and
prints the URL.

  lectern up             Start it, open the browser
  lectern up --no-browser  Skip opening a browser (e.g. over SSH)
  lectern up --service   Also install a systemd user unit (macOS: launchd)
                            so it survives a reboot
`

// upCommand is deliberately independent of the terminal-first `lectern local`
// command: that one attaches your terminal to the console dashboard, this one
// gets a browser onto a working board as fast as possible. Both share the
// exact same runtime through localruntime.Ensure, so running either first
// (or both) never starts a second instance.
func upCommand(cfg *config.Config, args []string) error {
	service, noBrowser := false, false
	for _, a := range args {
		switch a {
		case "--service":
			service = true
		case "--no-browser":
			noBrowser = true
		case "--help", "-h":
			fmt.Print(upHelp)
			return nil
		default:
			return fmt.Errorf("usage: lectern up [--service] [--no-browser]")
		}
	}
	if os.Getenv("LECTERN_API") != "" || os.Getenv("AGENTDECK_API") != "" {
		return errors.New("up starts a local board, but a remote API is configured; run `lectern` to connect to it, or unset LECTERN_API and AGENTDECK_API to start locally")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	fmt.Println("Starting Lectern…")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	ep, err := localruntime.Ensure(ctx, binary, cfg)
	if err != nil {
		return fmt.Errorf("start local runtime: %w", err)
	}
	c := console.New(ep.URL, ep.Token)

	status, err := fetchOnboarding(c)
	if err != nil {
		// Not fatal: the runtime is up and the board is reachable either way.
		fmt.Fprintf(os.Stderr, "warning: could not read onboarding status: %v\n", err)
	} else {
		printOnboardingSummary(status)
	}

	if cwd, err := os.Getwd(); err == nil {
		if name, repoPath, ok := currentGitProject(cwd); ok {
			id, created, err := ensureLocalProject(c, name, repoPath)
			switch {
			case err != nil:
				fmt.Fprintf(os.Stderr, "warning: could not register %s as a project: %v\n", repoPath, err)
			case created:
				fmt.Printf("Registered %q (%s) as a project (id %d).\n", name, repoPath, id)
			default:
				fmt.Printf("%q is already a registered project (id %d).\n", repoPath, id)
			}
		}
	}

	if service {
		if msg, err := installService(binary); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not install a service: %v\n", err)
		} else {
			fmt.Println(msg)
		}
	}

	if !noBrowser {
		if err := openBrowser(ep.URL); err != nil {
			fmt.Fprintf(os.Stderr, "Could not open a browser automatically (%v).\n", err)
		}
	}
	fmt.Printf("\nLectern is running at %s\nNext: open it and press \"Start your first session\".\n", ep.URL)
	return nil
}

type onboardingStatus struct {
	Agents   []onboard.AgentCheck `json:"agents"`
	Tmux     onboard.EnvCheck     `json:"tmux"`
	Python   onboard.EnvCheck     `json:"python"`
	Git      onboard.EnvCheck     `json:"git"`
	Projects int                  `json:"projects"`
	Sessions int                  `json:"sessions"`
}

// fetchOnboarding reads GET /api/onboarding rather than re-running the
// detection locally: the server we just started already computed it (and is
// the same code the web app's first-run checklist reads), so this keeps CLI
// and browser answers identical instead of two implementations that can drift.
func fetchOnboarding(c *console.Client) (*onboardingStatus, error) {
	data, err := c.JSON("GET", "/onboarding", nil)
	if err != nil {
		return nil, err
	}
	var out onboardingStatus
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func printOnboardingSummary(s *onboardingStatus) {
	var found, missing []string
	for _, a := range s.Agents {
		if a.Found {
			found = append(found, a.Name)
		} else if a.Builtin {
			missing = append(missing, a.Name)
		}
	}
	if len(found) > 0 {
		fmt.Printf("Detected agent CLIs: %s\n", strings.Join(found, ", "))
	}
	if len(missing) > 0 {
		fmt.Printf("Not on PATH (install and re-run `lectern up` to pick them up): %s\n", strings.Join(missing, ", "))
	}
	if !s.Tmux.OK {
		fmt.Printf("tmux: %s — %s\n", s.Tmux.Detail, s.Tmux.Fix)
	}
	if !s.Python.OK {
		fmt.Printf("python3: %s — %s\n", s.Python.Detail, s.Python.Fix)
	}
	if !s.Git.OK {
		fmt.Printf("git: %s — %s\n", s.Git.Detail, s.Git.Fix)
	}
}

// currentGitProject reports whether dir looks like a git repository worth
// offering as a project: it does not shell out to git, since the only fact
// needed is "does .git exist here" (a plain directory, or the file a worktree
// or submodule leaves behind both count).
func currentGitProject(dir string) (name, repoPath string, ok bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", false
	}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
		return "", "", false
	}
	base := filepath.Base(abs)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "", "", false
	}
	return base, abs, true
}

type upTargetView struct {
	ID   int64  `json:"id"`
	Kind string `json:"kind"`
}

type upProjectView struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	RepoPath string `json:"repo_path"`
}

func localTargetID(c *console.Client) (int64, error) {
	data, err := c.JSON("GET", "/targets", nil)
	if err != nil {
		return 0, err
	}
	var targets []upTargetView
	if err := json.Unmarshal(data, &targets); err != nil {
		return 0, err
	}
	for _, t := range targets {
		if t.Kind == "local" {
			return t.ID, nil
		}
	}
	return 0, errors.New("no local target is registered")
}

func projectForRepo(c *console.Client, repoPath string) (id int64, found bool, err error) {
	data, err := c.JSON("GET", "/projects", nil)
	if err != nil {
		return 0, false, err
	}
	var projects []upProjectView
	if err := json.Unmarshal(data, &projects); err != nil {
		return 0, false, err
	}
	for _, p := range projects {
		if samePath(p.RepoPath, repoPath) {
			return p.ID, true, nil
		}
	}
	return 0, false, nil
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	if errA != nil {
		ra = filepath.Clean(a)
	}
	rb, errB := filepath.EvalSymlinks(b)
	if errB != nil {
		rb = filepath.Clean(b)
	}
	return ra == rb
}

// ensureLocalProject registers repoPath as a project on the local target,
// unless a project already points at it — running `lectern up` twice in the
// same repo must not create a duplicate.
func ensureLocalProject(c *console.Client, name, repoPath string) (id int64, created bool, err error) {
	if id, found, err := projectForRepo(c, repoPath); err != nil {
		return 0, false, err
	} else if found {
		return id, false, nil
	}
	targetID, err := localTargetID(c)
	if err != nil {
		return 0, false, err
	}
	data, err := c.JSON("POST", "/projects", map[string]any{
		"name": name, "target_id": targetID, "repo_path": repoPath,
	})
	if err != nil {
		return 0, false, err
	}
	var p upProjectView
	if err := json.Unmarshal(data, &p); err != nil {
		return 0, false, err
	}
	return p.ID, true, nil
}

// openBrowser best-effort opens the URL with the platform's default browser
// launcher. It never blocks and never treats failure as fatal to `lectern up`
// — the URL is printed either way.
func openBrowser(url string) error {
	if onboard.Headless() {
		return errors.New("no display detected")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
