package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// commandDoc is one command's help: `lectern help NAME` and `lectern NAME
// --help` print it, and `lectern help` lists every one by group.
type commandDoc struct {
	Name     string   // "up", or "local stop" for a subcommand
	Aliases  []string // other names that print the same help
	Group    string   // "" keeps it out of the overview (a subcommand)
	Synopsis string   // the overview's left column
	Summary  string   // the overview's right column, and the help's first line
	Usage    []string
	About    string
	Examples []string
}

const (
	groupStart    = "Getting started"
	groupSessions = "Sessions"
	groupProjects = "Projects & machines"
	groupAdvanced = "Advanced"
)

var helpGroups = []string{groupStart, groupSessions, groupProjects, groupAdvanced}

var commandDocs = []commandDoc{
	{
		Name: "up", Group: groupStart, Synopsis: "up",
		Summary: "Start Lectern on this computer and open it in your browser",
		Usage:   []string{"lectern up [--no-browser] [--service]"},
		About: `Starts your private Lectern (or reuses the one already running), adds the
current folder as a project when it is a git repository, and opens your
browser on "Start an agent", already signed in. Run it again any time: it
picks up agents you have installed since, and signs in another browser.

  --no-browser   Print the sign-in link instead of opening a browser (over SSH)
  --service      Also start Lectern when you log in: a systemd user unit on
                 Linux, a launchd agent on macOS, and on Windows an entry in
                 your account's startup list (the HKCU ...\CurrentVersion\Run
                 registry key; no administrator rights needed)`,
		Examples: []string{"cd ~/myapp && lectern up", "lectern up --no-browser"},
	},
	{
		Name: "claude", Aliases: []string{"codex", "gemini"}, Group: groupStart, Synopsis: "claude | codex | gemini",
		Summary: "Start an agent in this folder and talk to it right here",
		Usage:   []string{"lectern claude [--new | --attach] [--model NAME] [--resume]", "lectern codex  [--new | --attach] [--model NAME] [--resume]"},
		About: `Starts a session for this folder (or reuses the one already running) and
connects this terminal to it. A git folder becomes a project the first time.
The session keeps running when you leave; it is on the web page and your
phone too. Leave with Ctrl+] d, come back with the same command.

  --new          Always start a new session
  --attach       Only reuse a running one (fails if there is none)
  --model NAME   Pick the model, for example opus or sonnet
  --resume       Continue the agent's last conversation in this folder

Any agent added under Settings → Agents works the same way: lectern NAME.`,
		Examples: []string{"cd ~/myapp && lectern claude", "lectern codex --resume", "lectern claude --model opus --new"},
	},
	{
		Name: "demo", Group: groupStart, Synopsis: "demo",
		Summary: "Try Lectern with a demo agent that needs nothing installed",
		Usage:   []string{"lectern demo [--new]"},
		About: `Starts a stand-in agent in a throwaway folder and connects this terminal to
it — the same demo as "Try a demo agent" on the web page. It uses no AI:
type a message and it writes it to a file, asking you first, so you can try
approvals (Ctrl+] y allows), leaving and coming back, and reviewing a change.

  --new   Start another demo instead of going back to the running one`,
		Examples: []string{"lectern demo"},
	},
	{
		Name: "doctor", Group: groupStart, Synopsis: "doctor",
		Summary: "Check this computer and say how to fix anything missing",
		Usage:   []string{"lectern doctor"},
		About: `Checks Git and installed agents, optional tools (tmux and Python 3),
the built-in web terminal and push alerts, which server
your commands talk to, and whether agents can reach it to report status and
ask for approval. Every problem comes with the command that fixes it.

Exits 0 when Lectern can run an agent here, and 1 when something required is
missing or broken. Optional things never fail it.`,
		Examples: []string{"lectern doctor"},
	},
	{
		Name: "phone", Group: groupStart, Synopsis: "phone",
		Summary: "Connect your phone to this Lectern, with a QR code to pair it",
		Usage:   []string{"lectern phone [--no-qr]", "lectern phone --off"},
		About: `Prints a QR code that pairs your phone with this Lectern (the same sessions
you already have). Scan it with the phone's camera: the link pairs the phone
once and works for 5 minutes.

When Lectern already has an address a phone can reach (your tailnet, or a
server's network address) that one is used. Your private Lectern, which
otherwise only answers this computer, is made reachable on this computer's
Wi-Fi address instead. Only paired devices can use it from the network, but
the connection is not encrypted, so use it on a network you trust. It stops
when the private Lectern stops, or with --off.

Away from home, use Tailscale (install it on this computer and the phone) or
a relay (lectern relay, see docs/relay.md) instead.

  --off     Stop listening on the Wi-Fi address
  --no-qr   Print only the link`,
		Examples: []string{"lectern phone", "lectern phone --off"},
	},
	{
		Name: "update", Group: groupStart, Synopsis: "update",
		Summary: "Update this lectern to the latest release",
		Usage:   []string{"lectern update [--check] [--version vX.Y.Z]"},
		About: `Downloads the release for this computer from GitHub, checks it against the
release's published checksums, and replaces this program. A running local
Lectern keeps running its old version until you restart it
(lectern local stop, then lectern up).

  --check            Only say whether a newer release exists
  --version vX.Y.Z   Install that release instead of the latest

Installed with Homebrew, a .deb/.rpm or Scoop? Update with that tool instead;
lectern update says which.`,
		Examples: []string{"lectern update", "lectern update --check"},
	},
	{
		Name: "help", Aliases: []string{"--help", "-h"}, Group: groupStart, Synopsis: "help [COMMAND]",
		Summary:  "Show help for a command",
		Usage:    []string{"lectern help [COMMAND]", "lectern COMMAND --help"},
		Examples: []string{"lectern help", "lectern help restore"},
	},
	{
		Name: "console", Aliases: []string{"tui"}, Group: groupSessions, Synopsis: "(no command)",
		Summary: "Open the terminal dashboard: every session, live",
		Usage:   []string{"lectern", "lectern console [--plain]"},
		About: `The dashboard lists every session with a live preview. Enter connects to
one; ? shows every key. --plain gives a line-by-line menu instead, for
screen readers and pipes.`,
		Examples: []string{"lectern", "lectern console --plain"},
	},
	{
		Name: "attach", Group: groupSessions, Synopsis: "attach KIND ID",
		Summary:  "Connect this terminal to a session",
		Usage:    []string{"lectern attach session ID", "lectern attach attempt ID"},
		About:    "Leave with Ctrl+] d; the session keeps running. Press Ctrl+] to see the other keys; Ctrl+] m opens Lectern's menu while connected.",
		Examples: []string{"lectern attach session 4"},
	},
	{
		Name: "restore", Group: groupSessions, Synopsis: "restore [QUERY|ID]",
		Summary: "Reopen a closed or interrupted session",
		Usage:   []string{"lectern restore [QUERY|ID] [--last] [--agent NAME] [--model M] [--profile ID] [--all] [--no-attach]"},
		About: `With nothing else, lists what can be restored, newest first. A word
restores the one closed session that matches it; a number restores that
session. When Lectern cannot tell which saved conversation to continue, it
shows them so you can pick one. --agent continues it in another agent,
primed with its last handoff or the end of its conversation.`,
		Examples: []string{"lectern restore", "lectern restore --last", "lectern restore parser", "lectern restore 42 --agent codex"},
	},
	{
		Name: "shell", Group: groupSessions, Synopsis: "shell [MACHINE]",
		Summary:  "Open a plain shell that keeps running, on a machine",
		Usage:    []string{"lectern shell [MACHINE]"},
		Examples: []string{"lectern shell", "lectern shell buildbox"},
	},
	{
		Name: "split", Group: groupSessions, Synopsis: "split",
		Summary:  "Open a shell beside the session you are connected to",
		Usage:    []string{"lectern split [--session current|ID|KIND/ID] [--dir agent|workdir] [--pick]"},
		About:    "Opens on the session's machine, in the agent's current folder (--dir workdir: the session's own folder).",
		Examples: []string{"lectern split", "lectern split --session 4 --dir workdir"},
	},
	{
		Name: "controls", Group: groupSessions, Synopsis: "controls [KIND ID]",
		Summary:  "Lectern's session menu, without opening another terminal",
		Usage:    []string{"lectern controls [KIND ID] [--popup] [--action upload]"},
		Examples: []string{"lectern controls session 4"},
	},
	{
		Name: "promote", Group: groupSessions, Synopsis: "promote SESSION-ID",
		Summary:  "Attach a running conversation to a project",
		Usage:    []string{"lectern promote SESSION-ID"},
		Examples: []string{"lectern promote 12"},
	},
	{
		Name: "local", Group: groupProjects, Synopsis: "local status | stop",
		Summary: "See or stop your private Lectern on this computer",
		Usage: []string{
			"lectern local status [--json]   Is it running, where, which version",
			"lectern local stop              Stop it (refused while tasks are running;",
			"                                sessions keep running and are picked up again)",
			"lectern local [COMMAND ...]     Run a command against it even when a",
			"                                Lectern service also runs on this computer",
		},
		About: `Your private Lectern starts by itself the first time a command needs it,
on a free port on 127.0.0.1 that only you can use. Its data lives in
~/.local/state/lectern/local.`,
		Examples: []string{"lectern local status", "lectern local stop && lectern up", "lectern local claude"},
	},
	{Name: "local status", Summary: "Is your private Lectern running, where, which version", Usage: []string{"lectern local status [--json]"}},
	{Name: "local stop", Summary: "Stop your private Lectern", Usage: []string{"lectern local stop"},
		About: "Refused while tasks are running. Sessions keep running and are picked up again at the next start."},
	{
		Name: "serve", Group: groupProjects, Synopsis: "serve",
		Summary: "Run Lectern as a server for other devices",
		Usage:   []string{"lectern serve [--insecure-listen]"},
		About: `Runs the control plane on LECTERN_PORT (default 9110). With no sign-in
configured it only listens on 127.0.0.1. To reach it from other devices, set
up a sign-in first: Tailscale identity (automatic when Tailscale is running)
or LECTERN_AUTH_TOKEN. It refuses to listen on the network without one unless
you pass --insecure-listen (LECTERN_INSECURE_LISTEN=1), for when something in
front of it already checks who is asking.

For just this computer, lectern up is simpler.`,
		Examples: []string{"lectern serve", "LECTERN_AUTH_TOKEN=$(openssl rand -hex 32) LECTERN_HOST=0.0.0.0 lectern serve", "LECTERN_MOCK=1 lectern serve"},
	},
	{
		Name: "files", Group: groupProjects, Synopsis: "files | upload | download",
		Summary:  "Browse, add or fetch files on an agent's machine",
		Usage:    []string{"lectern files KIND ID [PATH]", "lectern upload KIND ID FILE", "lectern download KIND ID REMOTE LOCAL"},
		About:    "KIND is session, attempt or project (upload also takes task). upload adds a file to the agent's context.",
		Examples: []string{"lectern files session 4", "lectern upload session 4 ./requirements.pdf", "lectern download session 4 out/report.html ./report.html"},
	},
	{Name: "upload", Summary: "Add a local file to an agent's context", Usage: []string{"lectern upload KIND ID FILE"}, Examples: []string{"lectern upload session 4 ./spec.pdf"}},
	{Name: "download", Summary: "Copy a file from an agent's machine", Usage: []string{"lectern download KIND ID REMOTE LOCAL"}, Examples: []string{"lectern download session 4 out/report.html ./report.html"}},
	{
		Name: "agent", Group: groupProjects, Synopsis: "agent list | save",
		Summary:  "List the agents Lectern can start, or replace your custom ones",
		Usage:    []string{"lectern agent list", "lectern agent save JSON|@FILE|-"},
		Examples: []string{"lectern agent list", "lectern agent save @agents.json"},
	},
	{
		Name: "account", Group: groupProjects, Synopsis: "account",
		Summary:  "Agent logins that Lectern can switch between at a usage limit",
		Usage:    []string{"lectern account list", "lectern account add AGENT LABEL [--machine NAME] [--dir PATH]", "lectern account login ID", "lectern account remove ID"},
		Examples: []string{"lectern account add claude work"},
	},
	{
		Name: "api", Group: groupAdvanced, Synopsis: "api METHOD PATH",
		Summary:  "Call Lectern's HTTP API",
		Usage:    []string{"lectern api METHOD /path [JSON|@FILE|-]"},
		About:    "Paths can leave out /api. JSON goes to stdout, errors to stderr.",
		Examples: []string{"lectern api GET /sessions", `lectern api POST /sessions/4/send '{"text":"Run the tests"}'`, `lectern api PATCH /routines/3 '{"enabled":false}'`},
	},
	{
		Name: "post", Group: groupAdvanced, Synopsis: "post FILE|URL",
		Summary:  "Show a recording, file or link in the Media feed",
		Usage:    []string{"lectern post FILE|URL [--title T] [--note N] [--session ID]"},
		Examples: []string{`lectern post ./demo.mp4 --title "Checkout flow passing"`, "lectern post http://127.0.0.1:5173 --title \"Dev server\""},
	},
	{
		Name: "live", Group: groupAdvanced, Synopsis: "live | expose",
		Summary:  "Watch a desktop, or reach a machine's port from your browser",
		Usage:    []string{"lectern live [URL] [--title T] [--machine NAME] [--session ID]", "lectern live list | lectern live stop ID", "lectern expose PORT [--title T] [--machine NAME] [--session ID]"},
		Examples: []string{`lectern live http://127.0.0.1:18080 --title "Watching the replay"`, `lectern expose 5173 --title "Dev server"`},
	},
	{Name: "expose", Summary: "Reach a machine's localhost:PORT from your own browser", Usage: []string{"lectern expose PORT [--title T] [--machine NAME] [--session ID]"}, Examples: []string{"lectern expose 5173"}},
	{
		Name: "browser", Group: groupAdvanced, Synopsis: "browser ACTION",
		Summary:  "Drive this session's browser (for agents)",
		Usage:    []string{strings.TrimPrefix(browserUsage, "usage: ")},
		Examples: []string{"lectern browser open http://127.0.0.1:5173", "lectern browser snapshot"},
	},
	{
		Name: "computer", Group: groupAdvanced, Synopsis: "computer ACTION",
		Summary: "Operate this session's live desktop, when allowed (for agents)",
		Usage:   []string{strings.TrimPrefix(computerUsage, "usage: ")},
	},
	{
		Name: "skill", Group: groupAdvanced, Synopsis: "skill",
		Summary: "Attach skills to a project's agents",
		Usage:   []string{"lectern skill list PROJECT [--agent AGENT]", "lectern skill attached PROJECT [--agent AGENT]", "lectern skill attach PROJECT SKILL_ID [--agent AGENT]", "lectern skill detach PROJECT ATTACHMENT_ID"},
	},
	{
		Name: "plugin", Group: groupAdvanced, Synopsis: "plugin",
		Summary:  "Find, install and manage plugins",
		Usage:    strings.Split(strings.TrimPrefix(pluginUsage, "usage:\n  "), "\n  "),
		About:    "See docs/plugins.md.",
		Examples: []string{"lectern plugin search", "lectern plugin new my-plugin"},
	},
	{
		Name: "mcp", Group: groupAdvanced, Synopsis: "mcp [--http]",
		Summary:  "Give an AI assistant Lectern's tools (MCP)",
		Usage:    []string{"lectern mcp          MCP on standard input/output", "lectern mcp --http   The same tools for claude.ai or ChatGPT, over OAuth"},
		Examples: []string{"claude mcp add lectern -- lectern mcp"},
	},
	{
		Name: "relay", Group: groupAdvanced, Synopsis: "relay",
		Summary: "Run an end-to-end encrypted relay for phones",
		Usage:   []string{"lectern relay [--listen ADDR] [flags]"},
		About:   "Needs LECTERN_RELAY_HOST_SECRET. `lectern relay --help` lists every flag. See docs/relay.md.",
	},
	{
		Name: "version", Aliases: []string{"--version", "-v"}, Group: groupAdvanced, Synopsis: "version",
		Summary: "Print this lectern's version",
		Usage:   []string{"lectern version"},
	},
}

// findDoc looks a command up by name or alias. words is the command line
// after `lectern`; "local stop" is tried before "local".
func findDoc(words []string) *commandDoc {
	var names []string
	for _, w := range words {
		if strings.HasPrefix(w, "-") && w != "--help" && w != "-h" && w != "--version" && w != "-v" {
			break
		}
		names = append(names, w)
	}
	for n := min(len(names), 2); n > 0; n-- {
		want := strings.Join(names[:n], " ")
		for i := range commandDocs {
			d := &commandDocs[i]
			if d.Name == want {
				return d
			}
			for _, a := range d.Aliases {
				if a == want {
					return d
				}
			}
		}
	}
	return nil
}

func printCommandHelp(w io.Writer, name string) bool {
	d := findDoc(strings.Fields(name))
	if d == nil {
		return false
	}
	writeCommandDoc(w, d)
	return true
}

func writeCommandDoc(w io.Writer, d *commandDoc) {
	fmt.Fprintf(w, "%s\n\nUsage:\n", d.Summary)
	for _, u := range d.Usage {
		fmt.Fprintf(w, "  %s\n", u)
	}
	if d.About != "" {
		fmt.Fprintf(w, "\n%s\n", d.About)
	}
	if len(d.Examples) > 0 {
		fmt.Fprintln(w, "\nExamples:")
		for _, e := range d.Examples {
			fmt.Fprintf(w, "  %s\n", e)
		}
	}
}

func printOverview(w io.Writer) {
	fmt.Fprint(w, `Lectern runs AI coding agents on your own machines, and lets you watch and
steer them from a browser, a terminal or your phone.

Usage: lectern [COMMAND] [ARGS]
`)
	for _, group := range helpGroups {
		fmt.Fprintf(w, "\n%s\n", group)
		for _, d := range commandDocs {
			if d.Group == group {
				fmt.Fprintf(w, "  %-26s %s\n", d.Synopsis, d.Summary)
			}
		}
	}
	fmt.Fprint(w, `
Examples:
  lectern up                     Start, and open the browser
  cd ~/myapp && lectern claude   Talk to Claude Code about this folder
  lectern help restore           Everything restore can do

Run "lectern help COMMAND" or "lectern COMMAND --help" for details.

Commands use your private Lectern on this computer. LECTERN_API=URL (with
LECTERN_AUTH_TOKEN) points them at a Lectern server elsewhere instead, and
"lectern doctor" says which one they use.
`)
}

// wantsHelp reports --help or -h among a command's own arguments (not after
// a bare "--", which passes everything on untouched).
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}

// helpCommand answers `lectern help ...`, `lectern --help` and `lectern
// COMMAND --help` before anything else runs, so asking for help never starts
// a runtime or needs a server. handled is false for anything else.
func helpCommand(args []string, stdout, stderr io.Writer) (code int, handled bool) {
	if len(args) == 0 {
		return 0, false
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		topic := args[1:]
		if len(topic) == 0 {
			printOverview(stdout)
			return 0, true
		}
		if d := docFor(topic); d != nil {
			writeCommandDoc(stdout, d)
			return 0, true
		}
		fmt.Fprintf(stderr, "lectern: no command called %q.%s Run \"lectern help\" to see them all.\n", strings.Join(topic, " "), didYouMean(topic[0]))
		return 2, true
	}
	if !wantsHelp(args[1:]) {
		return 0, false
	}
	if d := docFor(args); d != nil {
		writeCommandDoc(stdout, d)
		return 0, true
	}
	return 0, false
}

// docFor is findDoc, except that `local CMD` (which runs CMD against the
// private Lectern) is CMD's help unless local has its own entry for it.
func docFor(words []string) *commandDoc {
	d := findDoc(words)
	if d != nil && d.Name == "local" && len(words) > 1 && !strings.HasPrefix(words[1], "-") {
		if inner := findDoc(words[1:]); inner != nil {
			return inner
		}
	}
	return d
}

// commandSynonyms are words people reach for that are not Lectern's name for
// the thing, mapped to the command that does it.
var commandSynonyms = map[string]string{
	"pair": "phone", "mobile": "phone", "qr": "phone",
	"resume": "restore", "reopen": "restore",
	"dashboard": "console", "sessions": "console", "list": "console", "ls": "console",
	"start": "up", "open": "up",
	"upgrade": "update", "check": "doctor",
	"try": "demo", "tutorial": "demo",
}

// didYouMean suggests the known command closest to a mistyped one, as
// " Did you mean \"lectern restore\"?", or "" when nothing is close.
func didYouMean(word string) string {
	if word == "" {
		return ""
	}
	if name, ok := commandSynonyms[strings.ToLower(word)]; ok {
		return fmt.Sprintf(" Did you mean \"lectern %s\"?", name)
	}
	best, bestDist := "", 3
	var names []string
	for _, d := range commandDocs {
		names = append(names, append([]string{d.Name}, d.Aliases...)...)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.HasPrefix(name, "-") || strings.Contains(name, " ") {
			continue
		}
		dist := editDistance(strings.ToLower(word), name)
		if len(word) >= 3 && strings.HasPrefix(name, strings.ToLower(word)) {
			dist = 1
		}
		if dist < bestDist {
			best, bestDist = name, dist
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf(" Did you mean \"lectern %s\"?", best)
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
