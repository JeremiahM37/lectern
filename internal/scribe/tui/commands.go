package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// command is one slash command. Name may hold a subcommand ("git commit").
// Args is a hint for the argument form; a non-empty Args means the command
// wants arguments, so the palette and popup leave a trailing space.
type command struct {
	Name    string
	Aliases []string
	Args    string
	Desc    string
	Group   string
	Run     func(m *Model, args string) tea.Cmd
}

// Palette groups, in display order (spec 02 section 2).
var commandGroups = []string{"Session", "Git", "Models", "Intelligence", "Settings", "Tabs", "System"}

// commandTable is the registry. It is filled by init below.
var commandTable []*command

func registerCommands(cs ...*command) { commandTable = append(commandTable, cs...) }

// allCommands returns the registry in palette order.
func allCommands() []*command {
	out := make([]*command, 0, len(commandTable))
	for _, g := range commandGroups {
		for _, c := range commandTable {
			if c.Group == g {
				out = append(out, c)
			}
		}
	}
	return out
}

// matchCommands ranks commands against q (the text after "/").
func matchCommands(q string) []*command {
	type sc struct {
		c *command
		s int
	}
	var hits []sc
	for _, c := range allCommands() {
		best, ok := 0, false
		for _, name := range append([]string{c.Name}, c.Aliases...) {
			if s, hit := fuzzyScore(name, q); hit && (!ok || s > best) {
				best, ok = s, true
			}
		}
		if ok {
			hits = append(hits, sc{c, best})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].s > hits[j].s })
	out := make([]*command, len(hits))
	for i, h := range hits {
		out[i] = h.c
	}
	return out
}

// findCommand resolves a typed line ("/git commit msg") to its command and
// the argument text. The longest matching name wins.
func findCommand(line string) (*command, string) {
	line = strings.TrimSpace(strings.TrimPrefix(line, "/"))
	var best *command
	bestArgs := ""
	bestLen := -1
	for _, c := range commandTable {
		for _, name := range append([]string{c.Name}, c.Aliases...) {
			if line == name || strings.HasPrefix(line, name+" ") {
				if len(name) > bestLen {
					best, bestLen = c, len(name)
					bestArgs = strings.TrimSpace(line[len(name):])
				}
			}
		}
	}
	return best, bestArgs
}

// runCommand executes a "/..." line.
func (m *Model) runCommand(line string) tea.Cmd {
	c, args := findCommand(line)
	if c == nil {
		name := strings.Fields(line)
		first := line
		if len(name) > 0 {
			first = name[0]
		}
		m.enterChat()
		m.cur().chat.AddSystem("info", "Unknown command: "+first+". Type /help for the command list.")
		return nil
	}
	return c.Run(m, args)
}

// sendPrompt sends text as a turn, or steers the running one.
func (m *Model) sendPrompt(text string) tea.Cmd {
	t := m.cur()
	if t.busy {
		if t.be.Steer(text) {
			t.steerQ = append(t.steerQ, text)
			return nil
		}
	}
	return m.startTurn(t, text)
}

// flashCmd is a command that reports its result as a flash line.
func flashCmd(kind string, text string) tea.Cmd {
	return func() tea.Msg { return flashMsg{kind: kind, text: text} }
}

func errFlash(err error) tea.Cmd { return flashCmd("err", err.Error()) }

func init() {
	registerCommands(
		// ---- Session
		&command{Name: "new", Desc: "Start a new session (saves the current one)", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd { return m.newSession() }},
		&command{Name: "clear", Desc: "Clear the view (the session is kept)", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd { m.cur().chat.Clear(); return nil }},
		&command{Name: "sessions", Aliases: []string{"history"}, Desc: "Browse and resume sessions", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd { m.openSessionPicker(); return nil }},
		&command{Name: "resume", Args: "[id]", Desc: "Resume a session by id", Group: "Session",
			Run: func(m *Model, args string) tea.Cmd {
				if args == "" {
					m.openSessionPicker()
					return nil
				}
				return m.resumeSession(args)
			}},
		&command{Name: "fork", Desc: "Fork the current session into a new one", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd {
				t := m.cur()
				id, err := t.be.Fork()
				if err != nil {
					return errFlash(err)
				}
				t.chat.AddSystem("info", "Forked into session "+short(id))
				return flashCmd("ok", "Forked into "+short(id))
			}},
		&command{Name: "rename", Args: "[title]", Desc: "Rename the current session", Group: "Session",
			Run: func(m *Model, args string) tea.Cmd {
				if args == "" {
					m.pushModal(NewTextPrompt(m.th, "Rename session", "New title", false, func(v string) tea.Cmd {
						return m.renameCurrent(v)
					}).WithValue(m.cur().be.SessionTitle()))
					return nil
				}
				return m.renameCurrent(args)
			}},
		&command{Name: "export", Args: "[md|json]", Desc: "Export the session as markdown or JSON", Group: "Session",
			Run: func(m *Model, args string) tea.Cmd {
				format := "markdown"
				if strings.HasPrefix(args, "json") {
					format = "json"
				}
				path, err := m.cur().be.Export(format)
				if err != nil {
					return errFlash(err)
				}
				return flashCmd("ok", "Exported to "+path)
			}},
		&command{Name: "compact", Desc: "Shorten the conversation to free context", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd {
				be := m.cur().be
				m.setFlash("info", "Compacting context…")
				return func() tea.Msg {
					if err := be.Compact(context.Background()); err != nil {
						return flashMsg{kind: "err", text: "Compaction failed: " + err.Error()}
					}
					return flashMsg{kind: "ok", text: "Context compacted"}
				}
			}},
		&command{Name: "undo", Desc: "Undo the last turn and restore its files", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd { m.confirmUndo(); return nil }},
		&command{Name: "redo", Desc: "Redo the last undone turn", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd {
				cp, err := m.cur().be.Redo()
				if err != nil {
					return errFlash(err)
				}
				return flashCmd("ok", fmt.Sprintf("Redid turn %d", cp.Turn))
			}},
		&command{Name: "checkpoints", Desc: "List turns that changed files", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd { m.openCheckpoints(); return nil }},
		&command{Name: "plan", Desc: "Switch to plan mode (research and plan only)", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd { return m.setMode("plan") }},
		&command{Name: "auto", Desc: "Switch to auto mode (tool calls are approved)", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd { return m.setMode("auto") }},
		&command{Name: "default", Desc: "Switch to the default mode", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd { return m.setMode("default") }},
		&command{Name: "copy", Desc: "Copy the last answer to the clipboard (OSC 52)", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd { return m.copyLastAnswer() }},

		// ---- Git
		&command{Name: "git", Desc: "Git menu: status, diff, commit", Group: "Git",
			Run: func(m *Model, _ string) tea.Cmd { m.openGitMenu(); return nil }},
		&command{Name: "commit", Args: "[message]", Desc: "Commit staged changes (suggests a message)", Group: "Git",
			Run: func(m *Model, args string) tea.Cmd {
				if args != "" {
					return m.gitCommit(args)
				}
				return m.commitPrompt()
			}},
		&command{Name: "diff", Args: "[turn]", Desc: "Show the diff of a turn, or of the working tree", Group: "Git",
			Run: func(m *Model, args string) tea.Cmd {
				t := m.cur()
				if n, err := strconv.Atoi(args); err == nil {
					m.openInfo("Diff of turn "+args, "diff", strings.Split(t.be.TurnDiff(n), "\n"))
					return nil
				}
				m.openInfo("Working tree diff", "diff", strings.Split(t.be.GitDiff(), "\n"))
				return nil
			}},

		// ---- Models
		&command{Name: "model", Args: "[provider/model]", Desc: "Switch model (Ctrl+L opens the picker)", Group: "Models",
			Run: func(m *Model, args string) tea.Cmd {
				if args == "" {
					return m.openModelPicker()
				}
				return m.setModel(args)
			}},
		&command{Name: "models", Desc: "Pick a model from every provider", Group: "Models",
			Run: func(m *Model, _ string) tea.Cmd { return m.openModelPicker() }},
		&command{Name: "reasoning", Args: "[off|low|medium|high]", Desc: "Set how hard the model thinks", Group: "Models",
			Run: func(m *Model, args string) tea.Cmd {
				t := m.cur()
				if args == "" {
					return flashCmd("info", "Reasoning: "+t.be.Reasoning())
				}
				switch args {
				case "off", "low", "medium", "high":
					t.be.SetReasoning(args)
					return flashCmd("ok", "Reasoning: "+args)
				}
				return flashCmd("err", "Use /reasoning off, low, medium or high")
			}},
		&command{Name: "keys", Desc: "Add or change a provider API key", Group: "Models",
			Run: func(m *Model, _ string) tea.Cmd { m.openKeyPicker(); return nil }},

		// ---- Intelligence
		&command{Name: "context", Desc: "Context window and token use", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd {
				t := m.cur()
				used, win := t.be.Context()
				u, cost := t.be.Usage()
				lines := []string{
					fmt.Sprintf("Model          %s", t.be.Model()),
					fmt.Sprintf("Context        %s of %s (%d%%)", render.HumanTokens(used), render.HumanTokens(win), pct(used, win)),
					fmt.Sprintf("Input tokens   %s", render.HumanTokens(u.InputTokens)),
					fmt.Sprintf("Output tokens  %s", render.HumanTokens(u.OutputTokens)),
					fmt.Sprintf("Cache read     %s", render.HumanTokens(u.CacheReadTokens)),
					fmt.Sprintf("Cost           $%.4f", cost),
				}
				m.openInfo("Context", "gauge", lines)
				return nil
			}},
		&command{Name: "cost", Desc: "Token use and cost for this session", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd {
				u, cost := m.cur().be.Usage()
				lines := []string{
					fmt.Sprintf("Input     %s", render.HumanTokens(u.InputTokens)),
					fmt.Sprintf("Output    %s", render.HumanTokens(u.OutputTokens)),
					fmt.Sprintf("Cache     %s read, %s written", render.HumanTokens(u.CacheReadTokens), render.HumanTokens(u.CacheWriteTokens)),
					fmt.Sprintf("Cost      $%.4f", cost),
				}
				m.openInfo("Cost", "tokens", lines)
				return nil
			}},
		&command{Name: "memory", Desc: "Browse stored memories", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd {
				var lines []string
				for _, mem := range m.cur().be.Memories("") {
					pin := ""
					if mem.Pinned {
						pin = " ★"
					}
					lines = append(lines, fmt.Sprintf("[%s/%s]%s %s", mem.Scope, mem.Category, pin, mem.Title))
					if mem.Body != "" {
						lines = append(lines, "    "+oneLine(mem.Body))
					}
				}
				if len(lines) == 0 {
					lines = []string{"No memories yet."}
				}
				m.openInfo("Memory", "memory", lines)
				return nil
			}},
		&command{Name: "skills", Desc: "List available skills", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd { m.openSkills(); return nil }},
		&command{Name: "tools", Desc: "Enable or disable tools for the agent", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd { m.openTools(); return nil }},
		&command{Name: "mcp", Desc: "MCP servers and their state", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd { m.openServers("mcp"); return nil }},
		&command{Name: "lsp", Desc: "Language servers and their state", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd { m.openServers("lsp"); return nil }},
		&command{Name: "map", Args: "[rebuild]", Desc: "Code map statistics; rebuild re-indexes", Group: "Intelligence",
			Run: func(m *Model, args string) tea.Cmd {
				t := m.cur()
				if args == "rebuild" {
					t.be.RebuildMap()
					return flashCmd("info", "Rebuilding the code map…")
				}
				s := t.be.MapStats()
				lines := []string{
					fmt.Sprintf("Files     %d", s.Files),
					fmt.Sprintf("Symbols   %d", s.Symbols),
					fmt.Sprintf("Edges     %d", s.Edges),
				}
				for lang, n := range s.Languages {
					lines = append(lines, fmt.Sprintf("  %-9s %d", lang, n))
				}
				if s.Building {
					lines = append(lines, fmt.Sprintf("Building… %d%%", int(s.Progress*100)))
				}
				m.openInfo("Code map", "repomap", lines)
				return nil
			}},

		// ---- Settings
		&command{Name: "theme", Args: "[name]", Desc: "Switch colour theme (previews as you move)", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd {
				if args == "" {
					m.openThemePicker()
					return nil
				}
				return m.applyTheme(args)
			}},
		&command{Name: "verbose", Desc: "Toggle verbose tool output", Group: "Settings",
			Run: func(m *Model, _ string) tea.Cmd {
				t := m.cur()
				t.copt.Verbose = !t.copt.Verbose
				t.chat = rebuildTranscript(t.chat, t.copt, m.width, m.chatRows())
				return flashCmd("info", "Verbose output toggled")
			}},
		&command{Name: "reasoning-visibility", Desc: "Show or hide reasoning in the transcript", Group: "Settings",
			Run: func(m *Model, _ string) tea.Cmd {
				t := m.cur()
				t.copt.ShowReasoning = !t.copt.ShowReasoning
				t.chat = rebuildTranscript(t.chat, t.copt, m.width, m.chatRows())
				return flashCmd("info", "Reasoning visibility toggled")
			}},
		&command{Name: "settings", Desc: "View and change settings", Group: "Settings",
			Run: func(m *Model, _ string) tea.Cmd { m.openSettings(); return nil }},

		// ---- Tabs
		&command{Name: "tab", Args: "[new|close|rename <name>|N]", Desc: "Switch, open, close or rename tabs", Group: "Tabs",
			Run: func(m *Model, args string) tea.Cmd {
				switch {
				case args == "":
					m.openTabPicker()
				case args == "new":
					m.openNewTab()
				case args == "close":
					m.requestCloseTab()
				case strings.HasPrefix(args, "rename "):
					m.cur().name = strings.TrimSpace(strings.TrimPrefix(args, "rename "))
				default:
					if n, err := strconv.Atoi(args); err == nil {
						m.switchTab(n - 1)
					}
				}
				return nil
			}},
		&command{Name: "terminal", Args: "[command]", Desc: "Run a shell command in the project", Group: "Tabs",
			Run: func(m *Model, args string) tea.Cmd {
				if args != "" {
					return m.runShell(m.cur(), args)
				}
				m.pushModal(NewTextPrompt(m.th, "Run a command", "e.g. go test ./...", false, func(v string) tea.Cmd {
					return m.runShell(m.cur(), v)
				}))
				return nil
			}},
		&command{Name: "jobs", Desc: "Background shell jobs", Group: "Tabs",
			Run: func(m *Model, _ string) tea.Cmd {
				var lines []string
				for _, j := range m.cur().be.Jobs() {
					lines = append(lines, fmt.Sprintf("%s  %-8s %s", j.ID, j.Status, j.Command))
					for _, l := range j.Tail {
						lines = append(lines, "    "+l)
					}
				}
				if len(lines) == 0 {
					lines = []string{"No background jobs."}
				}
				m.openInfo("Jobs", "terminal", lines)
				return nil
			}},

		// ---- System
		&command{Name: "help", Aliases: []string{"?"}, Desc: "Keys and commands", Group: "System",
			Run: func(m *Model, _ string) tea.Cmd { m.openHelp(); return nil }},
		&command{Name: "init", Desc: "Ask Scribe to write SCRIBE.md for this project", Group: "System",
			Run: func(m *Model, _ string) tea.Cmd {
				return m.sendPrompt("Look around this project and write SCRIBE.md at its root: what it is, how to build and test it, the layout, and conventions a new contributor must know. Keep it short and factual.")
			}},
		&command{Name: "review", Desc: "Ask Scribe to review uncommitted changes", Group: "System",
			Run: func(m *Model, _ string) tea.Cmd {
				return m.sendPrompt("Review my uncommitted changes (git diff). Look for bugs, missing error handling and tests that should exist. Report findings, most serious first.")
			}},
		&command{Name: "errors", Desc: "Error log for this session", Group: "System",
			Run: func(m *Model, _ string) tea.Cmd { m.openErrorLog(); return nil }},
		&command{Name: "bug", Desc: "Print diagnostics to attach to a bug report", Group: "System",
			Run: func(m *Model, _ string) tea.Cmd {
				t := m.cur()
				lines := []string{
					"Scribe " + m.version,
					"Workdir   " + t.be.Workdir(),
					"Model     " + t.be.Model(),
					"Mode      " + t.be.Mode(),
					"Session   " + t.be.SessionID(),
					fmt.Sprintf("Errors    %d this session", len(m.errLog)),
					"",
					"Include the lines above and what you were doing.",
				}
				m.openInfo("Bug report details", "bug", lines)
				return nil
			}},
		&command{Name: "quit", Aliases: []string{"q"}, Desc: "Save and quit", Group: "System",
			Run: func(m *Model, _ string) tea.Cmd { return m.beginShutdown() }},
		&command{Name: "exit", Desc: "Save and quit", Group: "System",
			Run: func(m *Model, _ string) tea.Cmd { return m.beginShutdown() }},
	)
}

// ---------------------------------------------------------------- helpers

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func pct(used, win int) int {
	if win <= 0 {
		return 0
	}
	return used * 100 / win
}

// resumeSession loads a saved session into the active tab.
func (m *Model) resumeSession(id string) tea.Cmd {
	t := m.cur()
	if err := t.be.Resume(id); err != nil {
		m.logError(t, err.Error())
		return errFlash(err)
	}
	m.replayTranscript(t)
	m.setFlash("ok", "Resumed "+t.be.SessionTitle())
	return nil
}

// renameCurrent renames the active session.
func (m *Model) renameCurrent(title string) tea.Cmd {
	t := m.cur()
	if err := t.be.Rename(title); err != nil {
		return errFlash(err)
	}
	t.chat.AddSystem("info", "Renamed session to: "+title)
	return flashCmd("ok", "Renamed")
}

// setModel switches the active tab's model.
func (m *Model) setModel(ref string) tea.Cmd {
	t := m.cur()
	if err := t.be.SetModel(ref); err != nil {
		return errFlash(err)
	}
	t.model = t.be.Model()
	t.local = isLocalModel(t.model)
	return flashCmd("ok", "Model: "+t.model)
}

// applyTheme sets the theme everywhere and in the backend.
func (m *Model) applyTheme(name string) tea.Cmd {
	th, ok := theme.Lookup(name)
	if !ok {
		return flashCmd("err", "No theme named "+name)
	}
	m.setTheme(name, th)
	m.cur().be.SetTheme(name)
	return flashCmd("ok", "Theme: "+name)
}

// setTheme swaps the palette on the Model and every transcript.
func (m *Model) setTheme(name string, th theme.Theme) {
	m.themeName = name
	m.th = th
	for _, t := range m.tabs {
		t.copt.Theme = th
		t.chat = rebuildTranscript(t.chat, t.copt, m.width, m.chatRows())
	}
}

// gitCommit commits with message in a goroutine.
func (m *Model) gitCommit(msg string) tea.Cmd {
	be := m.cur().be
	return func() tea.Msg {
		if err := be.GitCommit(msg); err != nil {
			return flashMsg{kind: "err", text: "Commit failed: " + err.Error()}
		}
		return flashMsg{kind: "ok", text: "Committed"}
	}
}

// commitPrompt asks the backend for a suggested message, then opens a prompt with it filled in.
func (m *Model) commitPrompt() tea.Cmd {
	be := m.cur().be
	m.setFlash("info", "Writing a commit message…")
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		sugg, err := be.SuggestCommitMessage(ctx)
		if err != nil {
			return flashMsg{kind: "err", text: "No suggestion: " + err.Error()}
		}
		return commitSuggestedMsg{text: sugg}
	}
}

// commitSuggestedMsg opens the commit prompt once a suggestion is ready.
type commitSuggestedMsg struct{ text string }

// copyLastAnswer writes the last answer to the terminal clipboard with OSC 52.
func (m *Model) copyLastAnswer() tea.Cmd {
	var last string
	for _, ev := range m.cur().be.Transcript() {
		if ev.Kind == agent.EvTurnDone && ev.Text != "" {
			last = ev.Text
		}
	}
	if last == "" {
		return flashCmd("info", "No answer to copy yet")
	}
	enc := base64.StdEncoding.EncodeToString([]byte(last))
	fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\a", enc)
	return flashCmd("ok", "Copied the last answer")
}

// beginShutdown starts the quit sequence.
func (m *Model) beginShutdown() tea.Cmd {
	return m.startShutdown()
}
