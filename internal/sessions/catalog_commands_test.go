package sessions

import (
	"strings"
	"testing"

	agentcfg "github.com/JeremiahM37/lectern/v2/internal/agents"
)

// catalogLaunch is what each catalog preset must run, written out by hand
// from the CLI's own --help (docs/agents.md, "Catalog verification") rather
// than derived from the preset, so a preset edit that drifts from the real
// flags fails here. Empty means the CLI has no such launch.
type catalogLaunch struct {
	fresh, resume, resumeID, fork string
}

var catalogLaunches = map[string]catalogLaunch{
	"opencode":     {"opencode --auto --model m1 --prompt 'fix it'", "opencode --continue", "opencode --session abc", "opencode --session abc --fork"},
	"cursor-agent": {"cursor-agent --force --model m1 'fix it'", "cursor-agent --continue", "cursor-agent --resume abc", ""},
	"copilot":      {"copilot --yolo --model m1 -i 'fix it'", "copilot --continue", "copilot --resume=abc", ""},
	"amp":          {"amp", "amp threads continue --last", "amp threads continue abc", ""},
	"qwen":         {"qwen --yolo -m m1 -i 'fix it'", "qwen --continue", "qwen --resume abc", "qwen --resume abc --fork-session"},
	"kimi":         {"kimi --auto -m m1", "kimi --continue", "kimi --session abc", ""},
	"goose":        {"GOOSE_MODE=auto goose session --model m1", "goose session --resume", "goose session --resume --session-id abc", "goose session --resume --session-id abc --fork"},
	"aider":        {"aider --yes-always --model m1", "aider --restore-chat-history", "", ""},
	"crush":        {"crush --yolo", "crush --continue", "crush --session abc", ""},
	"cline":        {"cline -i --auto-approve true -m m1 'fix it'", "", "cline -i --id abc", ""},
	"grok":         {"grok --permission-mode bypassPermissions -m m1 -- 'fix it'", "grok --continue", "grok --resume abc", "grok --resume abc --fork-session"},
	"antigravity":  {"agy --dangerously-skip-permissions --model m1 --prompt-interactive 'fix it'", "agy --continue", "agy --conversation abc", ""},
	"muse":         {"muse --trust-workspace --yolo --model m1", "muse --trust-workspace resume --last", "muse --trust-workspace resume abc", ""},
	"mimo":         {"mimo --trust --yolo -m m1 --prompt 'fix it'", "mimo --trust --continue", "mimo --trust --session abc", "mimo --trust --session abc --fork"},
	"devin":        {"devin --permission-mode dangerous --model m1 -- 'fix it'", "devin --continue", "devin --resume abc", ""},
	"droid":        {"droid --auto high 'fix it'", "droid --resume --last", "droid --resume abc", "droid --fork abc"},
	"kiro":         {"kiro-cli chat --trust-all-tools --model m1 'fix it'", "kiro-cli chat --resume", "kiro-cli chat --resume-id abc", ""},
	"auggie":       {"auggie -m m1 'fix it'", "auggie --continue", "auggie --resume abc", ""},
	"cn":           {"cn --auto 'fix it'", "cn --resume", "", "cn --fork abc"},
	"kilo":         {"kilo --auto -m m1 --prompt 'fix it'", "kilo --continue", "kilo --session abc", "kilo --session abc --fork"},
	"vibe":         {"vibe --trust --auto-approve 'fix it'", "vibe --trust --continue", "vibe --trust --resume abc", ""},
	"rovodev":      {"acli rovodev run --yolo", "acli rovodev run --restore", "acli rovodev run --restore abc", ""},
	"codebuff":     {"codebuff 'fix it'", "codebuff --continue", "codebuff --continue abc", ""},
	"command-code": {"command-code --trust --yolo -m m1 'fix it'", "command-code --trust --continue", "command-code --trust --resume abc", "command-code --trust --resume abc --fork-session"},
	"autohand":     {"autohand --unrestricted --model m1", "", "autohand resume abc", "autohand --fork abc"},
	"zcode":        {"zcode --mode yolo", "zcode --continue", "zcode --resume abc", ""},
	"pi":           {"pi --model m1 'fix it'", "pi --continue", "pi --session abc", "pi --fork abc"},
	"omp":          {"omp --auto-approve --model m1 'fix it'", "omp --continue", "omp --resume abc", ""},
	"hermes":       {"hermes chat --yolo -m m1 -q 'fix it'", "hermes chat --continue", "hermes chat --resume abc", ""},
	"openclaude":   {"openclaude --dangerously-skip-permissions --model m1 'fix it'", "openclaude --continue", "openclaude --resume abc", "openclaude --resume abc --fork-session"},
}

// TestCatalogPresetLaunchCommands covers every interactive preset's fresh
// launch (yolo, model and opening prompt together), resume-last, exact
// resume and fork.
func TestCatalogPresetLaunchCommands(t *testing.T) {
	for _, p := range Catalog() {
		if p.Group == CatalogGroupAdapters {
			continue // task-only ACP adapters have no interactive launch
		}
		want, ok := catalogLaunches[p.Name]
		if !ok {
			t.Errorf("%s: no expected launch commands — add it to catalogLaunches", p.Name)
			continue
		}
		fresh := p.invocation(Start{Workdir: "/w", Model: "m1", Prompt: "fix it", Yolo: true})
		if fresh != want.fresh {
			t.Errorf("%s fresh launch:\n got %s\nwant %s", p.Name, fresh, want.fresh)
		}
		check := func(kind, want string, supported bool, got string) {
			t.Helper()
			if !supported {
				if want != "" {
					t.Errorf("%s: %s expected %q but the preset does not offer it", p.Name, kind, want)
				}
				return
			}
			if want == "" {
				t.Errorf("%s: preset offers %s (%s) but the CLI has none", p.Name, kind, got)
			} else if got != want {
				t.Errorf("%s %s:\n got %s\nwant %s", p.Name, kind, got, want)
			}
		}
		check("resume", want.resume, len(p.ResumeArgs) > 0, p.invocation(Start{Workdir: "/w", Resume: true}))
		check("resume by id", want.resumeID, len(p.ResumeIDArgs) > 0, p.invocation(Start{Workdir: "/w", ResumeID: "abc"}))
		check("fork", want.fork, len(p.ForkArgs) > 0, p.invocation(Start{Workdir: "/w", ForkID: "abc"}))
	}
}

// A preset with no way to pass an opening message still launches cleanly and
// leaves the message to be typed once the pane settles.
func TestCatalogPromptIsTypedWhenTheCLICannotTakeIt(t *testing.T) {
	for _, name := range []string{"kimi", "goose", "aider", "muse", "rovodev", "autohand", "zcode"} {
		p, ok := FindCatalogPreset(name)
		if !ok {
			t.Fatalf("%s preset missing", name)
		}
		if p.TakesPrompt() {
			t.Errorf("%s: its CLI cannot take an interactive opening prompt", name)
		}
		if got := p.invocation(Start{Workdir: "/w", Prompt: "fix it"}); strings.Contains(got, "fix it") {
			t.Errorf("%s: prompt leaked onto the command line: %s", name, got)
		}
	}
}

// Yolo off must drop both the flag and the environment switch.
func TestCatalogYoloOffDropsFlagsAndEnv(t *testing.T) {
	for _, p := range Catalog() {
		got := p.invocation(Start{Workdir: "/w"})
		for _, arg := range p.YoloArgs {
			if strings.Contains(got, " "+arg) {
				t.Errorf("%s: yolo flag %q present with yolo off: %s", p.Name, arg, got)
			}
		}
		for k := range p.YoloEnv {
			if strings.Contains(got, k+"=") {
				t.Errorf("%s: yolo env %q present with yolo off: %s", p.Name, k, got)
			}
		}
	}
}

// catalogTasks is each Task-backed preset's background command for a
// bypassPermissions run with a model (built through the same Launcher as
// real tasks), plus the permission modes it must refuse.
var catalogTasks = map[string]struct {
	contains []string
	refuses  []string
}{
	"amp":          {[]string{"amp --stream-json -x"}, []string{"plan", "bypassPermissions"}},
	"aider":        {[]string{"aider --model m1 --yes-always --message"}, []string{"plan"}},
	"crush":        {[]string{"crush run"}, []string{"plan", "bypassPermissions"}},
	"antigravity":  {[]string{"agy --model m1 --dangerously-skip-permissions --print"}, nil},
	"muse":         {[]string{"muse exec --model m1 --yolo"}, []string{"plan"}},
	"droid":        {[]string{"droid exec --skip-permissions-unsafe"}, []string{"plan"}},
	"cn":           {[]string{"cn --auto -p"}, nil},
	"rovodev":      {[]string{"acli rovodev run --yolo"}, []string{"plan"}},
	"command-code": {[]string{"command-code -m m1 --yolo -p"}, nil},
	"zcode":        {[]string{"zcode --mode yolo --prompt"}, nil},
	"pi":           {[]string{"pi --model m1"}, []string{"bypassPermissions"}},
	"openclaude": {[]string{"openclaude --output-format stream-json --verbose --model m1 --permission-mode bypassPermissions -p"},
		nil},
}

func catalogTaskDefinition(p CatalogPreset) agentcfg.TaskDefinition {
	command := p.Task.Command
	if command == "" {
		command = p.Command
	}
	return agentcfg.TaskDefinition{Name: p.Name, Command: command, Args: p.Task.Args,
		ModelFlag: p.ModelFlag, PromptTemplate: p.Task.PromptTemplate, OutputMode: p.Task.OutputMode,
		PermissionArgs: p.Task.PermissionArgs, ResumeArgs: p.Task.ResumeArgs}
}

func TestCatalogPresetTaskCommands(t *testing.T) {
	for _, p := range Catalog() {
		if p.Task == nil {
			if _, listed := catalogTasks[p.Name]; listed {
				t.Errorf("%s: expected a task backend", p.Name)
			}
			continue
		}
		want, ok := catalogTasks[p.Name]
		if !ok {
			t.Errorf("%s: task-backed preset missing from catalogTasks", p.Name)
			continue
		}
		def := catalogTaskDefinition(p)
		mode := "bypassPermissions"
		for _, refused := range want.refuses {
			if refused == mode {
				mode = "acceptEdits"
			}
		}
		cmd, err := agentcfg.Launcher{}.Command(agentcfg.LaunchSpec{Agent: p.Name, Worktree: "/w",
			TmuxSession: "t", PermissionMode: mode, Model: "m1", Definition: &def})
		if err != nil {
			t.Errorf("%s: %v", p.Name, err)
			continue
		}
		for _, fragment := range want.contains {
			if !strings.Contains(cmd, fragment) {
				t.Errorf("%s task: missing %q in\n  %s", p.Name, fragment, cmd)
			}
		}
		for _, refused := range want.refuses {
			if _, err := (agentcfg.Launcher{}).Command(agentcfg.LaunchSpec{Agent: p.Name, Worktree: "/w",
				TmuxSession: "t", PermissionMode: refused, Definition: &def}); err == nil {
				t.Errorf("%s: permission mode %s should be refused — the CLI has no flag for it", p.Name, refused)
			}
		}
	}
}
