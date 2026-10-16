package exec

import "strings"

// fixRule turns a lint command into the same command with the tool's
// auto-fix applied. match is the first substring of the command to rewrite;
// replace is what it becomes. Rules are tried in order.
type fixRule struct {
	match   string
	replace string
}

// fixRules covers the lint commands detect.go produces. A lint command that
// matches no rule has no auto-fix, and project fix refuses it rather than
// guessing. Script-based lint (npm run lint) is not rewritten because the
// script hides the linter.
var fixRules = []fixRule{
	{"golangci-lint run", "golangci-lint run --fix"},
	{"biome check", "biome check --write"},
	{"oxlint", "oxlint --fix"},
	{"eslint", "eslint --fix"},
	{"ruff check", "ruff check --fix"},
	{"cargo clippy", "cargo clippy --fix --allow-dirty"},
	{"rubocop", "rubocop -a"},
	{"swiftlint", "swiftlint --fix"},
	{"dart analyze", "dart fix --apply"},
	{"dotnet format --verify-no-changes", "dotnet format"},
	{"vendor/bin/pint --test", "vendor/bin/pint"},
	{"zig fmt --check", "zig fmt"},
	{"spotlessCheck", "spotlessApply"},
	{"ktlintCheck", "ktlintFormat"},
}

// fixCommand returns lint with its auto-fix flags applied, or ok=false when
// the linter has no known fix.
func fixCommand(lint string) (fixed string, ok bool) {
	for _, r := range fixRules {
		if strings.Contains(lint, r.match) {
			return strings.Replace(lint, r.match, r.replace, 1), true
		}
	}
	return "", false
}

// legacyFixRules are the spellings older linter versions used for a fix flag
// that fixRules now writes. A run whose output says the flag is unknown is
// retried once with the legacy spelling (spec 06 section 9.2).
var legacyFixRules = []fixRule{
	{"biome check --write", "biome check --apply"},
	{"rubocop -a", "rubocop --auto-correct"},
	{"ktlint --format", "ktlint -F"},
}

// unknownFlagHint matches linter output that rejects a flag as unknown.
var unknownFlagHint = []string{
	"unknown option", "unrecognized option", "unrecognised option", "unknown flag",
	"unknown argument", "unexpected argument", "no such option", "flag provided but not defined",
	"invalid option", "unknown switch",
}

// legacyFixCommand returns fixed with its fix flag swapped for the legacy
// spelling, when the output shows the flag was rejected and a legacy spelling
// is known. ok is false otherwise.
func legacyFixCommand(fixed, output string) (string, bool) {
	low := strings.ToLower(output)
	rejected := false
	for _, h := range unknownFlagHint {
		if strings.Contains(low, h) {
			rejected = true
			break
		}
	}
	if !rejected {
		return "", false
	}
	for _, r := range legacyFixRules {
		if strings.Contains(fixed, r.match) {
			return strings.Replace(fixed, r.match, r.replace, 1), true
		}
	}
	return "", false
}
