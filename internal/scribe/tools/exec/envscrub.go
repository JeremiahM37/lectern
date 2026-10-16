package exec

import "strings"

// allowedEnv names always pass scrubbing, even if a denied suffix would match.
// Anything not denied passes too: the denylist is the safety mechanism, this
// list documents the toolchain variables that must keep working.
var allowedEnv = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "USERNAME": true, "SHELL": true,
	"LANG": true, "TERM": true, "TERM_PROGRAM": true, "EDITOR": true, "VISUAL": true,
	"TMPDIR": true, "TMP": true, "TEMP": true, "PWD": true, "HOSTNAME": true,
	"GOPATH": true, "GOROOT": true, "GOBIN": true, "GOFLAGS": true,
	"CARGO_HOME": true, "RUSTUP_HOME": true, "NVM_DIR": true, "BUN_INSTALL": true,
	"NODE_PATH": true, "PYTHONPATH": true, "VIRTUAL_ENV": true, "CONDA_PREFIX": true,
	"SSH_AUTH_SOCK": true, "GPG_TTY": true, "JAVA_HOME": true, "ANDROID_HOME": true,
	"SYSTEMROOT": true, "COMSPEC": true, "PATHEXT": true, "USERPROFILE": true,
	"APPDATA": true, "LOCALAPPDATA": true, "PROGRAMFILES": true, "WINDIR": true,
	"PSMODULEPATH": true,
}

// deniedSuffixes drop a variable whose name ends with them.
var deniedSuffixes = []string{"_KEY", "_TOKEN", "_SECRET", "_PASSWORD", "_CREDENTIAL", "_PRIVATE_KEY"}

// deniedPrefixes drop provider and cloud variables whatever their suffix.
var deniedPrefixes = []string{
	"ANTHROPIC_", "OPENAI_", "GEMINI_", "GOOGLE_API", "MISTRAL_", "DEEPSEEK_",
	"OPENROUTER_", "COHERE_", "AWS_", "AZURE_", "GITHUB_", "GITLAB_", "SLACK_",
	"STRIPE_", "DISCORD_", "TELEGRAM_", "HUGGINGFACE_", "NPM_CONFIG_",
}

// ScrubEnv returns environ with credentials removed, plus GIT_TERMINAL_PROMPT=0
// so git never blocks on an interactive credential prompt.
func ScrubEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			continue
		}
		if keepEnv(name) {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_TERMINAL_PROMPT=0")
}

// keepEnv reports whether a variable named name may reach a child process.
func keepEnv(name string) bool {
	up := strings.ToUpper(name)
	if allowedEnv[up] {
		return true
	}
	for _, p := range deniedPrefixes {
		if strings.HasPrefix(up, p) {
			return false
		}
	}
	for _, s := range deniedSuffixes {
		if strings.HasSuffix(up, s) {
			return false
		}
	}
	return true
}
