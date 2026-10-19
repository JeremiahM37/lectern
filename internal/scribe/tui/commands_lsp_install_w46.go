package tui

// /lsp install: lists the language servers for this project's languages, with
// whether each is on PATH and the exact install command. With a server name it
// installs that server, and only after the user confirms in a modal. The
// install runs off the event loop (runBusy) and its result is a system row.

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/lsp"
)

func init() {
	registerCommands(&command{Name: "lsp install", Args: "[server]",
		Desc:  "Language servers for this project, installed or not, with the install command; a server name installs it after you confirm",
		Group: "Intelligence",
		Run:   func(m *Model, args string) tea.Cmd { return lspInstallCommand(m, args) }})
}

// lspInstallRun runs one install command and returns its combined output. It
// is a variable so tests can replace it; no test runs a real install.
var lspInstallRun = func(ctx context.Context, argv []string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// lspInstallTimeout bounds one install. A go build of gopls is the slow case.
const lspInstallTimeout = 20 * time.Minute

// lspInstallCommand is the whole /lsp install command. It never runs an
// install itself: a runnable one only starts from the confirm modal's yes.
func lspInstallCommand(m *Model, args string) tea.Cmd {
	t := m.cur()
	say := func(kind, text string) {
		m.enterChat()
		t.chat.AddSystem(kind, text)
	}
	name := strings.TrimSpace(args)
	if name == "" {
		say("info", lspInstallListing(t.be.Workdir()))
		return nil
	}
	inst, ok := lsp.ServerInstallFor(name)
	if !ok {
		say("error", fmt.Sprintf("No language server named %s is catalogued. /lsp install lists them.", name))
		return nil
	}
	if p, found := lsp.Find(inst.Server); found {
		say("info", fmt.Sprintf("%s is already installed at %s.", inst.Server, p))
		return nil
	}
	if !inst.Runnable() {
		say("info", fmt.Sprintf("%s has no install this command runs. Install it yourself: %s", inst.Server, inst.Command()))
		return nil
	}
	if _, ok := lsp.Find(inst.Tool()); !ok {
		say("error", fmt.Sprintf("Cannot install %s: %s is not on PATH. Install %s, or run this yourself: %s",
			inst.Server, inst.Tool(), inst.Tool(), inst.Command()))
		return nil
	}
	server, cmdText, argv := inst.Server, inst.Command(), inst.Argv
	m.pushModal(NewConfirm(m.th, "Install "+server+"?",
		fmt.Sprintf("Runs: %s. It can take a few minutes.", cmdText), false,
		func() tea.Cmd {
			return m.runBusy("Installing "+server+"…", func() func() tea.Cmd {
				ctx, cancel := context.WithTimeout(context.Background(), lspInstallTimeout)
				defer cancel()
				out, err := lspInstallRun(ctx, argv)
				return lspInstallDone(t, server, cmdText, out, err)
			})
		}).WithLabels("Yes, install", "Cancel"))
	return nil
}

// lspInstallDone reports an install's outcome in the tab's chat. It runs on the
// event loop, as runBusy's apply step requires.
func lspInstallDone(t *tab, server, cmdText, out string, err error) func() tea.Cmd {
	return func() tea.Cmd {
		if err != nil {
			msg := fmt.Sprintf("Install of %s failed: %v", server, err)
			if tail := lspInstallTail(out, 6); tail != "" {
				msg += "\n" + tail
			}
			t.chat.AddSystem("error", msg)
			return flashCmd("err", "Install of "+server+" failed")
		}
		if p, ok := lsp.Find(server); ok {
			t.chat.AddSystem("info", fmt.Sprintf("Installed %s at %s.", server, p))
			return flashCmd("ok", "Installed "+server)
		}
		t.chat.AddSystem("info", fmt.Sprintf("%s finished, but %s is not on PATH yet. Add its install directory to PATH, then run /lsp restart.", cmdText, server))
		return flashCmd("info", server+" is not on PATH yet")
	}
}

// lspInstallListing is the text /lsp install shows with no server name.
func lspInstallListing(workdir string) string {
	langs := lsp.DetectLanguages(workdir)
	if len(langs) == 0 {
		return "No language with a known server was found in " + workdir +
			". To install one anyway, name it: /lsp install <server>."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Language servers for this project (%s):", strings.Join(langs, ", "))
	for _, e := range lsp.InstallCatalog(langs) {
		state := "not installed"
		if p, ok := lsp.Find(e.Server); ok {
			state = "installed (" + p + ")"
		}
		fmt.Fprintf(&b, "\n  %s  %s  %s\n    install: %s", e.Server, e.Language, state, e.Command())
	}
	b.WriteString("\nInstall one with /lsp install <server>. It asks before it runs anything.")
	return b.String()
}

// lspInstallTail returns the last n non-empty lines of an install's output.
func lspInstallTail(out string, n int) string {
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			keep = append(keep, s)
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, "\n")
}
