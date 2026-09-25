#!/usr/bin/env python3
"""Claude Code PreToolUse guard: refuse host tmux kill commands.

Hook JSON arrives on stdin. A Bash command that could kill sessions on the
operator's own tmux server is refused with exit 2; anything else is allowed.
The isolated test runners are exempt, because they tear down a private tmux
server inside a bubblewrap namespace rather than the live one.

See docs/known-failure-modes.md for the incident this prevents.
"""

import json
import re
import sys

KILL_SUBCOMMAND = re.compile(
    r"\btmux\b[^|;&\n]*\b("
    r"kill-server|kill-session|kill-window|kill-pane"
    r"|detach-client\s+-a|respawn-pane|respawn-window)\b"
)
KILL_PROCESS = re.compile(r"\b(pkill|killall)\b[^|;&\n]*\btmux\b")
ISOLATED_RUNNER = re.compile(r"run-isolated-tests|test-all-parallel")

EXPLANATION = """\
refusing to run this command: {command}
It can kill the operator's live tmux server.

Lectern runs every agent in tmux, so kill-server/kill-session (or pkill/killall
tmux) on this host ends all of them at once and cannot be undone. To exercise
tmux kill paths, run them through tools/run-isolated-tests.sh, which uses a
private tmux server in a bubblewrap namespace.

See docs/known-failure-modes.md.
"""


def main() -> int:
    try:
        payload = json.load(sys.stdin)
    except (OSError, ValueError):
        return 0  # never block on input we cannot read
    if not isinstance(payload, dict) or payload.get("tool_name") != "Bash":
        return 0
    tool_input = payload.get("tool_input")
    command = tool_input.get("command") if isinstance(tool_input, dict) else None
    if not isinstance(command, str):
        return 0
    if ISOLATED_RUNNER.search(command):
        return 0
    if KILL_SUBCOMMAND.search(command) or KILL_PROCESS.search(command):
        print(EXPLANATION.format(command=command), file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
