package exec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// ------------------------------------------------- shell pre-checks

// subshellRe matches $(...), `...` and ${...}. The body is the first
// non-empty capture group.
var subshellRe = regexp.MustCompile("\\$\\(([^)]*)\\)|`([^`]*)`|\\$\\{([^}]*)\\}")

// sensitiveWords are keywords that make an expansion suspicious.
var sensitiveWords = []string{"env", "pem", "key", "credentials", "secrets", "npmrc", "netrc",
	"htpasswd", "ssh", "token", "passwd", "shadow", "aws"}

// suspiciousSubshell returns the sensitive keyword found in a command
// substitution or parameter expansion, if any. Keywords match as substrings,
// as the spec does.
func suspiciousSubshell(command string) (string, bool) {
	for _, m := range subshellRe.FindAllStringSubmatch(command, -1) {
		body := ""
		for _, g := range m[1:] {
			if g != "" {
				body = strings.ToLower(g)
				break
			}
		}
		for _, kw := range sensitiveWords {
			if strings.Contains(body, kw) {
				return kw, true
			}
		}
	}
	return "", false
}

// cleanToken strips redirection operators and quotes from a shell word.
func cleanToken(t string) string {
	t = strings.TrimLeft(t, "<>&0123456789")
	return strings.Trim(t, `"'`)
}

// binaryReaders are commands whose file arguments are read as text.
var binaryReaders = map[string]bool{"cat": true, "head": true, "tail": true, "less": true,
	"more": true, "bat": true, "tac": true, "nl": true}

// binaryReadRefusal refuses a text reader pointed at a binary file. Pipelines
// are split so that each reader is checked on its own arguments.
func binaryReadRefusal(command, workdir string) (string, bool) {
	for _, seg := range regexp.MustCompile(`[|;&\n]`).Split(command, -1) {
		words := strings.Fields(seg)
		if len(words) == 0 {
			continue
		}
		if !binaryReaders[filepath.Base(words[0])] {
			continue
		}
		for _, w := range words[1:] {
			tok := cleanToken(w)
			if tok == "" || strings.HasPrefix(tok, "-") {
				continue
			}
			abs := tok
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(workdir, tok)
			}
			fi, err := os.Stat(abs)
			if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
				continue
			}
			if isBinaryFile(abs) {
				ext := strings.ToLower(filepath.Ext(tok))
				if ext == "" {
					ext = "no extension"
				}
				return fmt.Sprintf("Cannot read binary file: %s (%s, %s). Use a tool that handles this format.",
					tok, ext, humanBytes(fi.Size())), true
			}
		}
	}
	return "", false
}

// isBinaryFile reports whether the first 8 KB of path contain a NUL byte.
func isBinaryFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	for _, b := range buf[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// DispatchGuard is what the agent publishes under Services["dispatch"] while
// subagents may be editing the project. Git commands that change the
// repository are refused while it reports true.
type DispatchGuard interface {
	DispatchActive() bool
}

// dispatchActive reports whether a dispatch is running in env.
func dispatchActive(env *tools.Env) bool {
	g, ok := tools.Service[DispatchGuard](env, "dispatch")
	return ok && g != nil && g.DispatchActive()
}

const dispatchBlockedMsg = "BLOCKED: a dispatch is running subagents in this project. " +
	"Git commands that change the repository wait until it finishes; read-only git is fine."

// mutatingGit matches a shell git command that changes the repository.
var mutatingGit = regexp.MustCompile(`\bgit\s+(-\S+\s+)*(commit|stash|restore|reset|cherry-pick|rebase|checkout|switch|merge|pull|push|add|rm|clean|tag|am|apply|revert|branch\s+-[dD]|worktree)\b`)

// ------------------------------------------------ files a command writes

// writeRedirect matches > file and >> file.
var writeRedirect = regexp.MustCompile(`>>?\s*([^\s;&|<>]+)`)

// writtenFiles lists the paths a shell command writes, as the spec's claim
// rules read them: sed -i, cp, mv, tee and output redirections. Paths are
// returned as written; the caller resolves them.
func writtenFiles(command string) []string {
	var out []string
	for _, m := range writeRedirect.FindAllStringSubmatch(command, -1) {
		out = append(out, strings.Trim(m[1], `"'`))
	}
	for _, seg := range regexp.MustCompile(`[|;&\n]`).Split(command, -1) {
		words := strings.Fields(seg)
		if len(words) < 2 {
			continue
		}
		var args []string
		for _, w := range words[1:] {
			if !strings.HasPrefix(w, "-") {
				args = append(args, strings.Trim(w, `"'`))
			}
		}
		switch filepath.Base(words[0]) {
		case "tee":
			out = append(out, args...)
		case "cp", "mv":
			if len(args) >= 2 {
				out = append(out, args[len(args)-1])
			}
		case "sed":
			if hasInPlace(words[1:]) && len(args) >= 1 {
				out = append(out, args[len(args)-1])
			}
		}
	}
	seen := map[string]bool{}
	var uniq []string
	for _, p := range out {
		if p == "" || p == "/dev/null" || seen[p] {
			continue
		}
		seen[p] = true
		uniq = append(uniq, p)
	}
	return uniq
}

// removers are commands whose plain arguments are paths they delete or create.
// Their targets count toward the outside-the-project check (outsideWrites) but
// not toward checkpoint snapshots, which only hold file contents.
var removers = map[string]bool{"rm": true, "rmdir": true, "unlink": true, "touch": true, "mkdir": true, "truncate": true}

// removedFiles lists the path arguments of removers in command, so a plain
// `rm file` outside the project asks like a redirect there does.
func removedFiles(command string) []string {
	var out []string
	for _, seg := range regexp.MustCompile(`[|;&\n()]`).Split(command, -1) {
		words := strings.Fields(seg)
		if len(words) < 2 || !removers[filepath.Base(words[0])] {
			continue
		}
		// A recursive or forced rm already gets the destructive prompt (gate),
		// which names the outside target; asking twice would show two cards.
		if rmRule.MatchString(seg) {
			continue
		}
		for _, w := range words[1:] {
			if !strings.HasPrefix(w, "-") {
				out = append(out, strings.Trim(w, `"'`))
			}
		}
	}
	return out
}

// expandHome resolves a leading ~, $HOME or ${HOME} to the home directory.
// Without this "$HOME/x" is not absolute to Go and was joined to the working
// directory, so a write in the home directory looked like one in the project.
func expandHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	for _, pre := range []string{"${HOME}", "$HOME", "~"} {
		if p == pre {
			return home
		}
		if strings.HasPrefix(p, pre+"/") {
			return filepath.Join(home, p[len(pre)+1:])
		}
	}
	return p
}

// hasInPlace reports whether sed was given -i (with or without a suffix).
func hasInPlace(words []string) bool {
	for _, w := range words {
		if w == "-i" || strings.HasPrefix(w, "-i") || (strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.Contains(w, "i")) {
			return true
		}
	}
	return false
}

// ------------------------------------------------ pre-commit gate

// gitCommitMessage matches a commit that carries its message on the command
// line, which is the only form the pre-commit gate checks.
var gitCommitMessage = regexp.MustCompile(`\bgit\s+commit\b.*(\s-[a-zA-Z]*m\b|\s--message\b)`)

// nativeCheckTimeout bounds each pre-commit check.
const nativeCheckTimeout = 15 * time.Second

// nativeChecks picks the project's own lint and typecheck commands from its
// config files. Nothing is read from package.json scripts. A command whose
// program is not installed is left out, so a missing tool does not block the
// commit.
func nativeChecks(dir string) []string {
	has := func(names ...string) bool {
		for _, n := range names {
			if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
				return true
			}
		}
		return false
	}
	var cmds []string
	if has("biome.json", "biome.jsonc") {
		cmds = append(cmds, "biome check .")
	}
	if has("tsconfig.json") {
		cmds = append(cmds, "tsc --noEmit")
	}
	if has("Cargo.toml") {
		cmds = append(cmds, "cargo clippy", "cargo check")
	}
	if has("go.mod") {
		// Go: golangci-lint when configured, else go vet; then the build.
		if has(".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json") {
			cmds = append(cmds, "golangci-lint run")
		} else {
			cmds = append(cmds, "go vet ./...")
		}
		// Compile without writing the program into the project (see detect.go).
		cmds = append(cmds, "go build -o "+os.DevNull+" ./...")
	}
	if has("ruff.toml", ".ruff.toml") {
		cmds = append(cmds, "ruff check")
	}
	if has("pyrightconfig.json") {
		cmds = append(cmds, "pyright")
	}
	var out []string
	for _, c := range cmds {
		if programInstalled(dir, strings.Fields(c)[0]) {
			out = append(out, c)
		}
	}
	return out
}

// localProgram rewrites the program of cmd to its node_modules/.bin copy when
// the project has one. The login shell does not see node_modules/.bin on its
// PATH, so a bare "tsc" would not be found.
func localProgram(dir, cmd string) string {
	words := strings.Fields(cmd)
	if len(words) == 0 {
		return cmd
	}
	local := filepath.Join(dir, "node_modules", ".bin", words[0])
	if _, err := os.Stat(local); err != nil {
		return cmd
	}
	return "'" + strings.ReplaceAll(local, "'", `'\''`) + "'" + strings.TrimPrefix(cmd, words[0])
}

// programInstalled reports whether name is on PATH or in the project's
// node_modules/.bin.
func programInstalled(dir, name string) bool {
	if _, err := exec.LookPath(name); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Join(dir, "node_modules", ".bin", name))
	return err == nil
}

// preCommitLines is how many lines of a failing check the commit refusal shows.
const preCommitLines = 30

// headLines keeps the first n lines of s and says how many were left out.
func headLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n… %d more lines omitted", len(lines)-n)
}

// runPreCommit runs the project's native checks in order and returns a
// blocking result for the first one that fails or times out.
func runPreCommit(ctx context.Context, env *tools.Env, dir string) (tools.Result, bool) {
	for _, cmd := range nativeChecks(dir) {
		fg := runForegroundIn(ctx, env, dir, localProgram(dir, cmd), nativeCheckTimeout, []string{"FORCE_COLOR=0", "NO_COLOR=1"})
		if fg.err == nil && !fg.timedOut && !fg.cancelled && fg.exit == 0 {
			continue
		}
		body := headLines(strings.TrimRight(fg.body, "\n"), preCommitLines)
		if fg.timedOut {
			body += fmt.Sprintf("\n[%s timed out after %s]", cmd, nativeCheckTimeout)
		}
		if strings.TrimSpace(body) == "" {
			body = "(no output)"
		}
		return tools.Result{
			Output: fmt.Sprintf("Pre-commit check failed (%s):\n%s\nFix errors before committing. Use project(action: \"lint\", fix: true) to auto-fix.",
				cmd, body),
			IsError: true,
			Summary: "pre-commit check failed",
			Meta:    map[string]any{"pre_commit": cmd},
		}, true
	}
	return tools.Result{}, false
}

// ------------------------------------------------ toolchain hint

// toolchainPrograms are the commands that count as a toolchain call.
var toolchainPrograms = map[string]bool{"go": true, "cargo": true, "npm": true, "npx": true, "pnpm": true,
	"yarn": true, "bun": true, "bunx": true, "python": true, "python3": true, "pytest": true, "ruff": true,
	"pyright": true, "mypy": true, "tsc": true, "biome": true, "eslint": true, "make": true, "dotnet": true,
	"mvn": true, "gradle": true, "gradlew": true, "./gradlew": true, "golangci-lint": true, "rubocop": true,
	"swift": true, "mix": true, "uv": true, "vitest": true, "jest": true, "oxlint": true, "deno": true}

// toolchainWords map a word in the command to the project action it means.
var toolchainWords = map[string]string{"test": "test", "lint": "lint", "typecheck": "typecheck",
	"build": "build", "check": "check", "clippy": "lint", "vet": "lint", "fmt": "format", "format": "format"}

// toolchainHint returns the note that points the model at the project tool
// after a successful toolchain command, or "" when the command is not one.
func toolchainHint(command string) string {
	words := strings.Fields(command)
	if len(words) == 0 || !toolchainPrograms[filepath.Base(words[0])] && !toolchainPrograms[words[0]] {
		return ""
	}
	for _, w := range words[1:] {
		if action, ok := toolchainWords[strings.ToLower(w)]; ok {
			return fmt.Sprintf("Command succeeded. Next time use project(action: \"%s\") — it auto-detects the toolchain, results are structured, and output is visible in the UI.", action)
		}
	}
	return ""
}
