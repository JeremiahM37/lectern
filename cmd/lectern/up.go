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
			printCommandHelp(os.Stdout, "up")
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
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if _, running := localruntime.Peek(ctx); running {
		fmt.Println("Lectern is already running; checking for anything new…")
	} else {
		fmt.Println("Starting Lectern…")
	}
	ep, err := localruntime.Ensure(ctx, binary, cfg)
	if err != nil {
		return fmt.Errorf("start Lectern: %w", err)
	}
	if note := localruntime.OutdatedNote(ep.Build); note != "" {
		fmt.Println("Note: " + note)
	}
	// A runtime started earlier keeps the PATH it started with; hand it this
	// shell's, so an agent installed since then is found now.
	if _, err := localruntime.SharePath(ctx, ep); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not update Lectern's PATH: %v\n", err)
	}
	c := console.New(ep.URL, ep.Token)

	status, err := fetchOnboarding(c)
	if err != nil {
		// Not fatal: the runtime is up and the board is reachable either way.
		fmt.Fprintf(os.Stderr, "warning: could not check agents and tools: %v\n", err)
	} else {
		printOnboardingSummary(status)
	}

	// landing is where the browser opens: "Start an agent", with the project
	// for the folder `up` ran in already chosen when there is one.
	landing := "/#sessions/new"
	if cwd, err := os.Getwd(); err == nil {
		if name, repoPath, ok := currentGitProject(cwd); ok {
			id, created, err := ensureLocalProject(c, name, repoPath)
			if err == nil && id > 0 {
				landing = fmt.Sprintf("/#sessions/new/%d", id)
			}
			switch {
			case err != nil:
				fmt.Fprintf(os.Stderr, "warning: could not add %s as a project: %v\n", repoPath, err)
			case created:
				fmt.Printf("Project: added %q (%s).\n", name, repoPath)
			default:
				fmt.Printf("Project: %q is already added.\n", name)
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

	// The runtime only answers a signed-in browser (localruntime/gate.go);
	// this one-time link signs this browser in and lands on "Start an agent".
	link, err := localruntime.BrowserURL(ctx, ep, landing)
	if err != nil {
		return err
	}
	opened := false
	if !noBrowser {
		if err := openBrowser(link); err != nil {
			fmt.Fprintf(os.Stderr, "Could not open a browser (%v).\n", err)
		} else {
			opened = true
		}
	}
	fmt.Printf("\nLectern is running at %s\n", ep.URL)
	if opened {
		fmt.Println("Your browser is opening on \"Start an agent\".")
		fmt.Printf("If it didn't, open this link (it works once, for 10 minutes):\n  %s\n", link)
	} else {
		fmt.Printf("Open this link to sign in and start an agent (it works once, for 10 minutes):\n  %s\n", link)
	}
	// Only name an agent that is there to start.
	if agent := firstFoundAgent(status); agent != "" {
		fmt.Printf("Or stay in the terminal: cd into a project and run lectern %s.\n", agent)
	} else {
		fmt.Println("No agent yet? In the browser, \"Try a demo agent\" shows how it works with nothing installed.")
	}
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
	switch {
	case len(found) == 0:
		fmt.Println("Agents: none found. Install one, for example Claude Code:")
		fmt.Println("  npm install -g @anthropic-ai/claude-code")
		fmt.Println("then run lectern up again (no restart needed).")
	case len(missing) > 0:
		fmt.Printf("Agents: %s (not installed: %s — optional)\n", strings.Join(found, ", "), strings.Join(missing, ", "))
	default:
		fmt.Printf("Agents: %s\n", strings.Join(found, ", "))
	}
	for _, c := range []onboard.EnvCheck{s.Tmux, s.Git, s.Python} {
		if !c.OK && c.Name != "" {
			fmt.Printf("Missing %s (needed): %s\n", c.Name, c.Fix)
		}
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

func firstFoundAgent(s *onboardingStatus) string {
	if s == nil {
		return ""
	}
	for _, a := range s.Agents {
		if a.Found && a.Builtin {
			return a.Name
		}
	}
	return ""
}
