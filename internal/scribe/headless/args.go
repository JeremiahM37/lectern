package headless

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// errHelp is returned by parseArgs for --help and -h.
var errHelp = errors.New("help requested")

// options is the parsed command line.
type options struct {
	model, mode, cwd, system, session string
	prompt                            []string
	includes                          []string
	json, events, quiet, noRepoMap    bool
	diff, saveSession, chat           bool
	render, renderSet                 bool
	maxSteps                          int
	timeout                           time.Duration

	listProviders bool
	listModels    bool
	listModelsOf  string
	setKey        bool
	keyArgs       []string
	version       bool
}

// parseArgs reads the headless flags. Words that are not flags form the
// prompt. "--" ends flag parsing. Values may be given as "--flag value" or
// "--flag=value".
func parseArgs(args []string) (options, error) {
	var o options
	o.noRepoMap = os.Getenv("SCRIBE_NO_REPOMAP") == "1"
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			o.prompt = append(o.prompt, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			o.prompt = append(o.prompt, a)
			continue
		}
		name, val, hasVal := strings.Cut(a, "=")
		if name == "-s" {
			name = "--session"
		}
		if name == "-q" {
			name = "--quiet"
		}
		value := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 < len(args) && !(strings.HasPrefix(args[i+1], "-") && len(args[i+1]) > 1) {
				i++
				return args[i], nil
			}
			return "", fmt.Errorf("%s needs a value", name)
		}
		flag := func(set *bool) error {
			if hasVal {
				return fmt.Errorf("%s takes no value", name)
			}
			*set = true
			return nil
		}
		var err error
		switch name {
		case "--headless", "-p", "--print":
			// Accepted for callers that pass them straight through.
		case "--model":
			o.model, err = value()
		case "--mode":
			o.mode, err = value()
			if err == nil && !validMode(o.mode) {
				err = fmt.Errorf("unknown mode %q (use one of: %s)", o.mode, strings.Join(agent.Modes, ", "))
			}
		case "--cwd":
			o.cwd, err = value()
		case "--system":
			o.system, err = value()
		case "--include":
			var p string
			if p, err = value(); err == nil {
				o.includes = append(o.includes, p)
			}
		case "--session", "--resume":
			o.session, err = value()
		case "--max-steps":
			var v string
			if v, err = value(); err == nil {
				n, convErr := strconv.Atoi(v)
				if convErr != nil || n <= 0 {
					err = fmt.Errorf("--max-steps wants a positive number, got %q", v)
				}
				o.maxSteps = n
			}
		case "--timeout":
			var v string
			if v, err = value(); err == nil {
				o.timeout, err = parseTimeout(v)
			}
		case "--json":
			err = flag(&o.json)
		case "--events":
			err = flag(&o.events)
		case "--chat":
			err = flag(&o.chat)
		case "--no-repomap":
			err = flag(&o.noRepoMap)
		case "--diff":
			err = flag(&o.diff)
		case "--save-session":
			err = flag(&o.saveSession)
		case "--quiet":
			err = flag(&o.quiet)
		case "--render":
			err = flag(&o.render)
			o.renderSet = true
		case "--no-render":
			o.renderSet = true
			o.render = false
			err = flag(new(bool))
		case "--list-providers":
			err = flag(&o.listProviders)
		case "--list-models":
			o.listModels = true
			if hasVal {
				o.listModelsOf = val
			} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				o.listModelsOf = args[i]
			}
		case "--set-key":
			o.setKey = true
			if hasVal {
				err = fmt.Errorf("--set-key takes two arguments: PROVIDER KEY")
				break
			}
			for len(o.keyArgs) < 2 && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				o.keyArgs = append(o.keyArgs, args[i])
			}
		case "--version", "-v":
			err = flag(&o.version)
		case "--help", "-h":
			return o, errHelp
		default:
			err = fmt.Errorf("unknown flag %s", name)
		}
		if err != nil {
			return o, err
		}
	}
	if o.chat && o.events && o.json {
		return o, errors.New("--json and --events cannot be combined")
	}
	return o, nil
}

func validMode(m string) bool {
	for _, known := range agent.Modes {
		if m == known {
			return true
		}
	}
	return false
}

// parseTimeout takes milliseconds ("600000") or a Go duration ("10m").
func parseTimeout(v string) (time.Duration, error) {
	if ms, err := strconv.Atoi(v); err == nil {
		if ms < 0 {
			return 0, fmt.Errorf("--timeout must not be negative")
		}
		return time.Duration(ms) * time.Millisecond, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("--timeout wants milliseconds or a duration such as 90s, got %q", v)
	}
	return d, nil
}

const usage = `Scribe, Lectern's coding agent, without the terminal UI.

Usage:
  lectern code -p PROMPT [flags]        one prompt, text answer on stdout
  echo PROMPT | lectern code [flags]    prompt from stdin when it is not a terminal
  lectern code --chat [flags]           line-based chat on stdin until EOF
  lectern code --list-providers
  lectern code --list-models [PROVIDER]
  lectern code --set-key PROVIDER KEY

Flags:
  --model PROVIDER/MODEL   model to use (default: config, then the first provider with a key)
  --mode MODE              default, architect, socratic, challenge, plan or auto
  --json                   print one JSON object (chat: one object per turn)
  --events                 print JSONL events (see lectern help code)
  --chat                   multi-turn chat; a line submits, a trailing \ continues
  --quiet, -q              no progress on stderr
  --max-steps N            stop after N model steps (exit 1)
  --timeout DURATION       abort after this long: milliseconds or 90s, 10m (exit 2)
  --cwd DIR                project folder (default: current folder)
  --system TEXT            extra instructions for this run
  --include FILE           prepend a file to the prompt (repeatable)
  --no-repomap             skip the code map (also SCRIBE_NO_REPOMAP=1)
  --diff                   list the files this run edited (text mode)
  --save-session           keep the session so it can be resumed
  --session ID, --resume ID, -s ID
                           continue a saved session (id prefix)
  --render, --no-render    render the final Markdown (default: on for a terminal)
`
