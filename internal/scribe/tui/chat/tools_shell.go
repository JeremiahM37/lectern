package chat

import (
	"path"
	"strings"
)

// scriptRuntimes are the interpreters whose inline code a shell call runs.
var scriptRuntimes = map[string]bool{
	"node": true, "bun": true, "deno": true, "tsx": true, "ts-node": true,
	"python": true, "python3": true, "ruby": true, "perl": true, "lua": true,
	"php": true, "swift": true,
}

// inlineCode reports whether a shell command is an interpreter run with
// inline code (-e, -c or eval). Such a call shows as code execution rather
// than a shell command. It returns the runtime name and the code.
func inlineCode(command string) (runtime, code string, ok bool) {
	fields := strings.Fields(command)
	if len(fields) < 3 {
		return "", "", false
	}
	rt := path.Base(fields[0])
	if !scriptRuntimes[rt] {
		return "", "", false
	}
	for i := 1; i < len(fields)-1; i++ {
		switch fields[i] {
		case "-e", "-c", "eval":
			return rt, strings.Join(fields[i+1:], " "), true
		}
	}
	return "", "", false
}

// codeRowArgs is the args of an inline-code shell row: "runtime: first line",
// with the first line taken up to a literal \n and clipped to 50.
func codeRowArgs(runtime, code string) string {
	first := strings.SplitN(code, `\n`, 2)[0]
	first = strings.Trim(strings.TrimSpace(first), `'"`)
	return clip(runtime+": "+first, 50)
}
