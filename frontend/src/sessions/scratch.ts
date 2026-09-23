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

// The generic name a blank shell is created with. Kept in one place so the
// board can tell a card nobody has named from one somebody has: renaming is a
// deliberate act, and only a name that differs from this one is a person's.
export function scratchDefaultName(session: SessionView): string {
  return session.target_name ? `Shell · ${session.target_name}` : "Shell";
}

// The scratch directory's own name. It is unique (the target made it with
// mktemp) and it does not change while the shell runs, so two quick shells on
// the same machine are told apart by it instead of by two identical
// "Shell · <target>" titles.
export function scratchFolder(session: SessionView): string {
  const path = (session.workdir || session.workspace?.path || "").replace(
    /\/+$/,
    "",
  );
  return path.split("/").filter(Boolean).pop() || "";
}

// The title a scratch card shows. An explicit rename wins; otherwise the
// folder name, with the row id as a stable last resort when the target
// reported no usable path. Nothing here writes to the tracked row.
export function scratchTitle(session: SessionView): string {
  const name = (session.name || "").trim();
  if (name && name !== scratchDefaultName(session)) return name;
  return scratchFolder(session) || `Shell #${session.id}`;
}
