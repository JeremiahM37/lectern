import type { SessionView } from "../types";

// Presentation only: the tracked rows and their terminals are untouched.
//
// A scratch terminal is a blank shell with no project — the quick shell opened
// from Terminal's "New terminal", `lectern shell`, or the TUI's blank
// persistent shell. A project-less *agent* session is different: it is work
// that has not been named yet, so it stays with the sessions.
export function isScratchTerminal(session: SessionView): boolean {
  return session.agent === "shell" && !session.project_id;
}
