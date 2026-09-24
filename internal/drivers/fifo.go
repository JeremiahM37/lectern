package drivers

import (
	"encoding/json"
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// The two streaming drivers (claude-steer, codex-appserver) both need a
// process whose stdin can receive more than one message over the run's whole
// lifetime, on a target reached only through executor.Executor.Run/ReadFile —
// there is no persistent pipe to hold open across separate SSH/pct
// invocations. A plain FIFO does not solve this on its own: `claude < fifo`
// opens the fifo once, and the moment whichever process first writes to it
// closes, the reader sees EOF and the agent's stdin closes with it — fine for
// one message, wrong for steering.
//
// pumpScript is the fix: a small long-lived relay that owns the fifo's read
// end, reopening it after every writer disconnects instead of exiting on
// their EOF, and forwarding each line to its own stdout (the agent's real
// stdin) until it sees the explicit end sentinel. Each steer message is then
// a completely independent, short-lived `printf ... >> fifo` (see
// appendCommand), exactly the kind of one-shot command Executor.Run already
// does for everything else — no new executor capability was needed.
const pumpScript = `import sys
path = sys.argv[1]
END = "\x1eLECTERN_END\x1e"
while True:
    with open(path, "r") as f:
        for line in f:
            line = line.rstrip("\n")
            if line == END:
                sys.exit(0)
            if line == "":
                continue
            sys.stdout.write(line + "\n")
            sys.stdout.flush()
`

// endSentinel closes a streaming run's input: the pump sees it, exits, and
// its exit closes the agent's stdin, which is what lets the agent process
// itself exit and the wrapper write exit_code — the same finalisation path
// every other attempt already goes through. Not NUL (0x00): that byte cannot
// survive as a process argument (exec(2) argv strings are NUL-terminated),
// so it would corrupt the very shell command that appends it. 0x1E (ASCII
// Record Separator) is not a byte a real chat message would ever contain and
// carries no such restriction.
const endSentinel = "\x1eLECTERN_END\x1e"

// ensureFifoCommand creates the fifo synchronously, before the agent process
// ever starts. It must run to completion and return before either the tmux
// launch or the first append: `tmux new-session -d` returns as soon as the
// pane's shell is spawned, not once it has reached `mkfifo`, so folding fifo
// creation into that same launched command race against the caller's first
// write — losing that race turns steer.fifo into an ordinary regular file
// (`>>` on a missing path creates one), which never blocks and never behaves
// like a fifo again. Running it as its own prior Executor.Run call removes
// the race instead of narrowing it.
func ensureFifoCommand(rt string) string {
	return fmt.Sprintf("mkdir -p %s && { [ -p %s/steer.fifo ] || mkfifo %s/steer.fifo; }",
		shellq.Quote(rt), rt, rt)
}

// streamLaunchCommand renders the tmux wrapper for a streaming driver: cd
// into the worktree, run the pump (see ensureFifoCommand for why the fifo
// itself is not created here) piped into the agent command, with the
// agent's own stdout/stderr/exit_code redirected exactly like every other
// attempt (agents.Launcher.Command does the same three redirects).
//
// agentCmd is ONLY the agent invocation (env prefix + binary + flags), never
// "cd WT && agent" pre-joined: `&&` binds looser than `|` in POSIX shell
// grammar, so "cd WT && CMD1 | CMD2" already parses as "cd WT && (CMD1 |
// CMD2)" — precisely the grouping this needs (both pipeline stages fork with
// WT as their inherited cwd) — but "CMD0 | cd WT && CMD1" (what an
// already-joined "cd WT && agent" string produces once THIS function's own
// pipe is prepended) parses as "(CMD0 | cd WT) && CMD1" instead: the agent
// then runs with the pty's own stdin, never the fifo, and never receives a
// message again. Verified against a real tmux pane's process tree
// (`ps -ef`), not assumed from the grammar alone — that split silently
// stranded the agent on the wrong stdin with no error anywhere.
func streamLaunchCommand(session, rt, worktree, agentCmd string) string {
	inner := fmt.Sprintf(
		"cd %s && python3 %s/pump.py %s/steer.fifo | { %s; } > %s/events.jsonl 2> %s/stderr.log; echo $? > %s/exit_code",
		shellq.Quote(worktree), rt, rt, agentCmd, rt, rt, rt)
	return "tmux new-session -d -s " + session + " " + shellq.Quote(inner)
}

// appendCommand appends one line to the fifo. A short-lived writer is exactly
// what the pump's reopen loop expects (see pumpScript); it does not need to
// coordinate with anything else that might be appending concurrently, since
// each call is its own `open, write, close`.
func appendCommand(rt, line string) string {
	return fmt.Sprintf("printf '%%s\\n' %s >> %s/steer.fifo", shellq.Quote(line), rt)
}

// closeCommand sends the end sentinel, asking the run to finish gracefully
// once the agent is done with whatever it is currently doing.
func closeCommand(rt string) string {
	return appendCommand(rt, endSentinel)
}

// userMessageLine renders one user message in Claude Code's streaming input
// shape. Verified against `claude --help` (--input-format stream-json) and
// Claude Code's documented streaming-input format: each stdin line is a JSON
// object `{"type":"user","message":{"role":"user","content":[...]}}}`, the
// same envelope the SDK uses for a multi-turn headless session.
func userMessageLine(text string) string {
	raw, _ := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	})
	return string(raw)
}
