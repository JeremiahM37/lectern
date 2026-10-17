package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// PromptInput is everything the system prompt is built from. Keep it
// stable within a session: the prompt is cached by the provider and every
// change costs a cache miss.
type PromptInput struct {
	Workdir      string
	Date         string // YYYY-MM-DD, fixed at session start
	Branch       string
	ModelRef     string
	Instructions []Instruction
	Skills       string // skills catalog, one line per skill
	MemoryIndex  string // counts of saved memories per store (memory.Stores.Index)
	Deferred     string // deferred tool catalog, one line per tool (may be empty)
	CodeMap      string // rendered code map (may be empty)
	Extra        string // --system text
	Subagent     string // "" main agent, else "explore" / "code" / "web" / "desloppify"
	// GitHub overrides the gh availability check. nil means detect it once
	// per process (gh --version succeeds).
	GitHub *bool
	// Commands is the "Project commands" line (lint, typecheck, test, build)
	// from the project's detected profile. Empty when none is detected.
	Commands string
	// GitContext is the git block: branch, working-tree counts, merge
	// conflicts and recent commits. Empty outside a repository. The app
	// computes it through the exec package, which caches it briefly.
	GitContext string
}

// Instruction is one loaded project instruction file.
type Instruction struct {
	Path string
	Text string
	// Scope is "global" for the Scribe home file and "project" for files
	// under the project root. Empty means project scope.
	Scope string
	// Name is the file as the prompt labels it: relative to the project root
	// for project files, the base name for global ones. Empty falls back to Path.
	Name string
}

// label is how the prompt names this file, e.g. "project:AGENTS.md".
func (i Instruction) label() string {
	name := i.Name
	if name == "" {
		name = i.Path
	}
	if i.Scope == "global" {
		return "global:" + name
	}
	return "project:" + name
}

// Shared identity and voice. Every family opens with identityPrompt.
const identityPrompt = `You are Scribe, a coding agent running inside Lectern. You work in the user's repository through tools: you read code, search it, edit files, and run commands. You are direct, careful and brief.`

const howYouWork = `# How you work
- Understand before you change. Look at the code map and search before reading whole files; read the parts you will change before you edit them.
- Make the smallest change that fully solves the problem. Match the surrounding code's style, naming, comment density and idioms. Do not reformat code you did not need to touch.
- Prefer editing existing files to creating new ones. Never create documentation files unless asked.
- Validate input only at system boundaries (user input, external APIs, files you parse). Trust internal code paths.
- Delete dead code cleanly. Do not rename it to _unused and do not leave a comment saying it was removed.
- After changing code, check your work: run the project's typecheck, tests or build (the project tool knows how). If something fails, fix it in the same turn. Up to three focused attempts, then report what is blocking you.
- Do not narrate between tool calls. Call tools back to back; talk to the user when you have something to say.
- When you are done, give a short answer: what you changed (with file paths), how you verified it, and anything the user must decide or do. No preamble, no summary of the obvious.
- Reference code as path:line. Each finding reads as "path:line — finding. fix." Use fragments where the meaning is clear. Quote identifiers, paths, flags and error strings exactly.
- If a request is ambiguous in a way that matters, ask (ask_user) with concrete options. Otherwise make a reasonable assumption and say what it was.
- Never claim you ran or verified something you did not. Report failures plainly with the actual output.`

const toolsPrompt = `# Tools
- read accepts several paths at once; use it to read related files together. Results show line numbers; never include them in edits.
- edit_file needs old_string to match exactly once (or set replace_all). multi_edit applies several edits to one file atomically. write_file is for new files or full rewrites of files you have read.
- grep and glob search text and names. find_symbol, symbol_refs, code_impact and file_outline use the code map and are faster for code navigation. navigate and diagnostics use language servers when available.
- shell runs commands in the project directory. Long-running servers or watchers go in background mode; check them with shell_output.
- dispatch runs subagents in parallel: explore agents to investigate independent questions, code agents for independent, well-specified edits in different files. Give each a complete, self-contained task.
- task_list keeps a visible checklist for multi-step work. memory saves durable facts (decisions, conventions, gotchas) for future sessions; save only what will matter later.`

const safetyPrompt = `# Safety
- Destructive commands (deleting outside the project, force pushes, hard resets) need the user's approval; the tool will ask. When the user denies one, that is final for this request: do not rerun it or a variant, do not ask for it again another way, say it was not run and carry on. A denial is a choice, not a block in the tool; if the user later asks again, call the tool again so they can decide.
- Do not read or print secrets. Do not commit, push or open pull requests unless the user asked.`

// basePrompt is the full prompt for the default family: identity, how to
// work, tools and safety, in that order.
var basePrompt = identityPrompt + "\n\n" + howYouWork + "\n\n" + toolsPrompt + "\n\n" + safetyPrompt

// answerVoice is the whole prompt for models that get no tool guidance.
const answerVoice = `# Answer voice
Flat and direct. Lead with the answer. Put paths, identifiers, flags and error strings in verbatim form. Keep the length proportional to the work.`

// familyBlocks adds one tone block per model family after the base prompt.
var familyBlocks = map[string]string{
	"claude": `# Style
Be terse and imperative. Put independent tool calls in one block so they run together. Reversible actions (reading, editing project files, running tests) need no confirmation; destructive ones do.`,
	"openai": `# Style
Keep going until the task is done. Gather only the context you need and stop once you are about 70 percent sure of the change. Do not write progress preambles before tool calls. Fix the root cause, not the symptom, and keep the diff minimal.`,
	"google": `# Style
Follow these mandates in order:
1. Ground every claim in code you have opened. Never speculate about files you have not read.
2. When the user's request follows large pasted data, answer from the data and restate the request in one line.
3. Keep answers short and factual.`,
}

// ModelFamily names the prompt family for a model reference such as
// "anthropic/claude-x" or "openrouter/anthropic/claude-x". It looks at the
// last path segment only, so the provider name never decides the family.
// Families: claude, openai, google, deepseek-reasoner, default.
func ModelFamily(modelRef string) string {
	id := strings.ToLower(modelRef)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	switch {
	case strings.Contains(id, "reasoner"), strings.Contains(id, "deepseek") && strings.Contains(id, "r1"):
		return "deepseek-reasoner"
	case strings.Contains(id, "claude"), strings.Contains(id, "kimi"):
		return "claude"
	case strings.Contains(id, "gemini"):
		return "google"
	case strings.HasPrefix(id, "gpt"), strings.HasPrefix(id, "o1"), strings.HasPrefix(id, "o3"),
		strings.HasPrefix(id, "o4"), strings.Contains(id, "grok"), strings.Contains(id, "deepseek"):
		return "openai"
	}
	return "default"
}

// isFlash reports whether a model reference names a Gemini Flash model.
func isFlash(modelRef string) bool {
	id := strings.ToLower(modelRef)
	return strings.Contains(id, "gemini") && strings.Contains(id, "flash")
}

var yearPattern = regexp.MustCompile(`^(\d{4})-\d{2}-\d{2}`)

// temporalNote tells Gemini Flash to take the date from the environment
// rather than from its training data. The year comes from the session date
// when it is known.
func temporalNote(date string) string {
	if m := yearPattern.FindStringSubmatch(date); m != nil {
		return fmt.Sprintf("- Use the current date from the Date line and tool results; the current year is %s.", m[1])
	}
	return "- Use the current date from the Date line and tool results, not from memory."
}

var (
	ghOnce  sync.Once
	ghFound bool
)

// githubCLIAvailable runs gh --version once per process. The answer is cached
// so the prompt stays byte-stable for the whole session.
func githubCLIAvailable() bool {
	ghOnce.Do(func() {
		if _, err := exec.LookPath("gh"); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ghFound = exec.CommandContext(ctx, "gh", "--version").Run() == nil
	})
	return ghFound
}

// subagentRole returns the role text for a subagent kind, or "" for none.
func subagentRole(kind string) string {
	switch kind {
	case "explore":
		return "You are an explore agent: read-only. Do not edit files. Cite each finding as path:line."
	case "web":
		return "You are a web research agent: search, fetch the pages that matter, and report the answer with source URLs. Do not edit files."
	case "code":
		return "You are a code agent. Edit only the files your task names. Read the parts you will change, make the edit, and verify it. If the change needs a file another agent is editing, do not touch it: say so in your report with the exact edit you wanted."
	case "desloppify":
		return "You are a cleanup agent. Remove framework-behaviour tests, redundant type assertions, error handling for states that cannot happen, debug logging and dead code. Keep TODO markers, business logic and non-obvious comments. Read around each section you edit, make the change with multi_edit, and report what you removed and what you kept."
	}
	return ""
}

// ModeBanner is the overlay text for a mode, or "" for default and unknown
// modes. The agent sends it as a latched user message, not in the system
// prompt.
func ModeBanner(mode string) string { return modeOverlays[mode] }

var modeOverlays = map[string]string{
	"plan": `# Mode: plan
You are in plan mode: read-only. Investigate the code with read-only tools, then call the plan tool with a concrete, ordered plan (files per step, risks, how it will be verified). Do not edit files or run commands that change anything. If a key decision is open, ask with ask_user before planning. Once the user approves, you will implement the plan in this same turn. If asked to edit now, say once that plan mode only proposes a plan for approval, and offer to write one with the plan tool. Do not repeat the refusal.`,
	"architect": `# Mode: architect
You are in architect mode: read-only design. Investigate, then answer with: the module boundaries involved, the design you recommend, alternatives and their tradeoffs, risks, and a "Critical files" list. Do not edit files. If asked to edit or run something that changes state, say once, briefly, that this mode is read-only and offer the read-only output you can give instead (the plan, design, options or objections). Do not repeat the refusal, lecture, or tell the user to switch modes to get the edit.`,
	"socratic": `# Mode: socratic
You are in socratic mode: read-only. Investigate first, then present the one or two decisions that matter as options with evidence from the code, and let the user choose. Do not edit files. If asked to edit or run something that changes state, say once, briefly, that this mode is read-only and offer the read-only output you can give instead (the plan, design, options or objections). Do not repeat the refusal, lecture, or tell the user to switch modes to get the edit.`,
	"challenge": `# Mode: challenge
You are in challenge mode: read-only. Act as a sharp reviewer of the user's idea or the code: find the strongest specific objections, with evidence from the code, and propose better alternatives. Do not edit files. If asked to edit or run something that changes state, say once, briefly, that this mode is read-only and offer the read-only output you can give instead (the plan, design, options or objections). Do not repeat the refusal, lecture, or tell the user to switch modes to get the edit.`,
	"auto": `# Mode: auto
You are in auto mode: the user is away. Do not ask questions; make reasonable assumptions and record them in your final answer. Verify after each unit of work. Approvals are granted automatically, so be conservative with anything destructive.`,
}

// commitGuidance is how the agent words a commit when asked for one.
const commitGuidance = `When you commit (git commit or the git tool's commit):
- Subject: one line in the imperative, under 72 characters, saying what the change does ("Add retry to the fetch client", not "Fixed stuff" or "Updates").
- Body: only when the reason is not obvious from the diff, wrapped at 72 characters, explaining why rather than what.
- Write the message exactly as you intend it. Do not add Co-Authored-By or any other trailer, and do not add "Generated with" lines.
- Stage only the files that belong to this change. Never stage secret files (.env, keys, credentials).
- Do not amend, force-push or rewrite history unless the user asked.
- The project's native checks run before the commit and block it when they fail. Fix the reported errors and commit again; do not use no_verify to get past them.`

// Modes in the order Shift+Tab cycles them.
var Modes = []string{"default", "auto", "plan", "architect", "socratic", "challenge"}

// BuildSystem assembles the system prompt for mode. The result is memoised
// on a hash of every input, so repeated calls with the same input return the
// same bytes without rebuilding.
func BuildSystem(in PromptInput, mode string) string {
	key := promptKey(in, mode)
	promptMemo.mu.Lock()
	if s, ok := promptMemo.m[key]; ok {
		promptMemo.mu.Unlock()
		return s
	}
	promptMemo.mu.Unlock()

	s := buildSystem(in, mode)

	promptMemo.mu.Lock()
	if len(promptMemo.m) >= 16 {
		promptMemo.m = map[string]string{}
	}
	promptMemo.m[key] = s
	promptMemo.mu.Unlock()
	return s
}

var promptMemo = struct {
	mu sync.Mutex
	m  map[string]string
}{m: map[string]string{}}

// promptKey hashes every input BuildSystem reads.
func promptKey(in PromptInput, mode string) string {
	h := sha256.New()
	field := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
		h.Write([]byte{1})
	}
	gh := "auto"
	if in.GitHub != nil {
		gh = fmt.Sprint(*in.GitHub)
	}
	field(mode, in.Workdir, in.Date, in.Branch, in.ModelRef, in.Skills, in.Deferred, in.CodeMap, in.Extra, in.Subagent, gh, runtime.GOOS, runtime.GOARCH, in.Commands, in.GitContext, in.MemoryIndex)
	for _, ins := range in.Instructions {
		field(ins.Path, ins.Text, ins.Scope, ins.Name)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func buildSystem(in PromptInput, mode string) string {
	family := ModelFamily(in.ModelRef)
	reasoner := family == "deepseek-reasoner"

	var b strings.Builder
	if reasoner {
		// Identity and answer voice only: no tool guidance, no skills,
		// no deferred or code-map sections.
		b.WriteString(identityPrompt)
		b.WriteString("\n\n")
		b.WriteString(answerVoice)
	} else {
		b.WriteString(basePrompt)
		if block := familyBlocks[family]; block != "" {
			b.WriteString("\n\n")
			b.WriteString(block)
		}
	}
	if in.Subagent != "" {
		if role := subagentRole(in.Subagent); role != "" {
			b.WriteString("\n\n# Role\nYou are a subagent working on one task given by the lead agent. You cannot talk to the user. Work only on your task, then finish with a concise report the lead can act on: findings with path:line references, or the changes you made and how you verified them. Do not repeat the task back.\n")
			b.WriteString(role)
		}
	}
	// The mode banner is not part of the system prompt: it is latched into
	// the conversation as a user message when the mode changes (see
	// modeNotice), so the system prompt stays byte-identical across modes.
	b.WriteString("\n\n# Environment\n")
	fmt.Fprintf(&b, "- Working directory: %s (shell commands already run here; do not prepend cd)\n", in.Workdir)
	fmt.Fprintf(&b, "- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if in.Branch != "" {
		fmt.Fprintf(&b, "- Git branch: %s\n", in.Branch)
	}
	if in.Commands != "" {
		fmt.Fprintf(&b, "- Project commands: %s. Use the project tool for these; it also takes cwd for a monorepo package.\n", in.Commands)
	}
	if in.Date != "" {
		fmt.Fprintf(&b, "- Date: %s\n", in.Date)
	}
	if in.ModelRef != "" {
		fmt.Fprintf(&b, "- Model: %s\n", in.ModelRef)
	}
	if !reasoner && isFlash(in.ModelRef) {
		b.WriteString(temporalNote(in.Date))
		b.WriteString("\n")
	}
	gh := githubCLIAvailable()
	if in.GitHub != nil {
		gh = *in.GitHub
	}
	if gh {
		b.WriteString("- GitHub CLI (gh) is installed: use it for issues, pull requests and releases.\n")
	}
	if !reasoner && in.GitContext != "" {
		b.WriteString("\n# Git context\nThe state of the repository when this turn started. It is a snapshot; run git status for the current state.\n")
		b.WriteString(in.GitContext)
		b.WriteString("\n")
	}
	if !reasoner && (in.Subagent == "" || in.Subagent == "code") {
		b.WriteString("\n# Commit messages\n" + commitGuidance + "\n")
	}
	writeInstructions(&b, in.Instructions)
	if !reasoner && in.Deferred != "" {
		b.WriteString("\n# Deferred tools\nThese tools are not loaded yet. Call request_tools with their names to load them; they can be called from the next step. Load only what the task needs.\n")
		b.WriteString(in.Deferred)
		b.WriteString("\n")
	}
	if in.MemoryIndex != "" {
		b.WriteString("\n# Memory\nSaved memories for this project and your preferences. Search or list them with the memory tool before relying on what you remember:\n")
		b.WriteString(in.MemoryIndex)
		b.WriteString("\n")
	}
	if !reasoner && in.Skills != "" {
		b.WriteString("\n# Skills\nLoad a skill with the skills tool when the task matches it:\n")
		b.WriteString(in.Skills)
		b.WriteString("\n")
	}
	if in.Extra != "" {
		b.WriteString("\n# Additional instructions\n")
		b.WriteString(in.Extra)
		b.WriteString("\n")
	}
	if !reasoner && in.CodeMap != "" {
		b.WriteString("\n# Code map\nThe most central files and their key symbols, ranked by how much of the codebase depends on them:\n\n")
		b.WriteString(in.CodeMap)
		b.WriteString("\n")
	}
	return b.String()
}

// writeInstructions emits the project instruction block. When any global
// file is present the block opens with a precedence preamble, and global
// files come first so project files, which sit nearer the code, win.
func writeInstructions(b *strings.Builder, ins []Instruction) {
	if len(ins) == 0 {
		return
	}
	hasGlobal := false
	for _, i := range ins {
		if i.Scope == "global" {
			hasGlobal = true
		}
	}
	b.WriteString("\n# Project instructions\n")
	if hasGlobal {
		b.WriteString("Global instructions apply everywhere. Where a project file conflicts with a global one, the project file wins.\n")
	}
	for _, i := range ins {
		fmt.Fprintf(b, "\n[%s]\n%s\n", i.label(), strings.TrimSpace(i.Text))
	}
}

// InstructionSource is a named family of instruction files. The first
// existing non-empty candidate of each source is used, per directory.
type InstructionSource struct {
	ID    string
	Label string
	Files []string
}

// InstructionSources is the catalogue of sources config can enable by id.
var InstructionSources = []InstructionSource{
	{ID: "scribe", Label: "Scribe", Files: []string{"SCRIBE.md", ".scribe/SCRIBE.md", ".scribe/instructions.md"}},
	{ID: "soulforge", Label: "SoulForge", Files: []string{"SOULFORGE.md", ".soulforge/SOULFORGE.md", ".soulforge/instructions.md"}},
	{ID: "claude", Label: "Claude", Files: []string{"CLAUDE.md", ".claude/instructions.md"}},
	{ID: "codex", Label: "Codex", Files: []string{"AGENTS.md", ".agents/instructions.md"}},
	{ID: "cursorrules", Label: "Cursor", Files: []string{".cursorrules", ".cursor/rules", ".cursor/rules.md"}},
	{ID: "github-copilot", Label: "GitHub Copilot", Files: []string{".github/copilot-instructions.md"}},
	{ID: "cline", Label: "Cline", Files: []string{".clinerules", ".cline/rules"}},
	{ID: "windsurf", Label: "Windsurf", Files: []string{".windsurfrules"}},
	{ID: "aider", Label: "Aider", Files: []string{".aider.conf.yml", ".aiderignore"}},
	{ID: "amp", Label: "Amp", Files: []string{"AMPLIFY.md", ".amp/instructions.md"}},
}

// DefaultInstructionFiles are the sources loaded when config does not say
// otherwise. The order matters: within a directory, earlier sources come
// first in the prompt.
var DefaultInstructionFiles = []string{"scribe", "codex", "claude"}

// instructionSource finds a source by id.
func instructionSource(id string) (InstructionSource, bool) {
	for _, s := range InstructionSources {
		if s.ID == id {
			return s, true
		}
	}
	return InstructionSource{}, false
}

// maxInstructionBytes caps one instruction file.
const maxInstructionBytes = 40 << 10

// LoadInstructions reads instruction files for a project. names is a list
// of source ids (see InstructionSources) and/or literal file names; a name
// that is not a source id is read as a file name in every directory. A nil
// list means DefaultInstructionFiles.
//
// Project scope is root and its parents, nearest last so it wins. Global
// scope is home/instructions.md, loaded only when home is a different
// directory from root. Each source contributes its first existing non-empty
// candidate per directory. Files are capped at 40 KB and identified by
// realpath, so a symlink does not load twice.
func LoadInstructions(root, home string, names []string) []Instruction {
	if names == nil {
		names = DefaultInstructionFiles
	}
	var out []Instruction
	seen := map[string]bool{}
	// take appends abs if it is an existing non-empty file. It reports
	// whether the candidate exists, so the caller knows to stop trying the
	// source's later candidates. A file already loaded counts as existing.
	take := func(abs, scope, name string) bool {
		b, err := os.ReadFile(abs)
		if err != nil || len(strings.TrimSpace(string(b))) == 0 {
			return false
		}
		id := abs
		if r, err := filepath.EvalSymlinks(abs); err == nil {
			id = r
		}
		if seen[id] {
			return true
		}
		seen[id] = true
		if len(b) > maxInstructionBytes {
			b = append(b[:maxInstructionBytes], []byte("\n…(truncated)")...)
		}
		out = append(out, Instruction{Path: abs, Text: string(b), Scope: scope, Name: name})
		return true
	}
	if home != "" && filepath.Clean(home) != filepath.Clean(root) {
		if p, err := filepath.Abs(filepath.Join(home, "instructions.md")); err == nil {
			take(p, "global", "instructions.md")
		}
	}
	var dirs []string
	for d := root; d != ""; {
		dirs = append([]string{d}, dirs...)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	for _, d := range dirs {
		for _, n := range names {
			candidates := []string{n}
			if src, ok := instructionSource(n); ok {
				candidates = src.Files
			}
			for _, c := range candidates {
				p, err := filepath.Abs(filepath.Join(d, c))
				if err != nil {
					continue
				}
				if take(p, "project", relName(root, p)) {
					break // first existing non-empty candidate of this source
				}
			}
		}
	}
	return out
}

// relName is p relative to root, with forward slashes, for the prompt label.
func relName(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return p
}
