// "Race N agents" (docs/guide): the same prompt to N attempts, each in its
// own worktree, landing in Compare so a person (or the judge) picks one.
export type RaceMode = "mixed" | "same";

// raceAgents returns the agent for each attempt: attempt 1 is primary; in
// mixed mode the rest are the other available agents, in order, repeating
// once every one has a turn.
export function raceAgents(n: number, primary: string, available: string[], mode: RaceMode): string[] {
  const count = Math.max(2, Math.min(8, Math.floor(n)));
  if (mode === "same") return Array.from({ length: count }, () => primary);
  const pool = [primary, ...available.filter((a) => a !== primary)];
  const unique = pool.filter((a, i) => pool.indexOf(a) === i);
  return Array.from({ length: count }, (_, i) => unique[i % unique.length]!);
}

// availableAgents reads which built-in CLIs a machine's last probe found.
export function availableAgents(infoJSON: string | undefined): string[] {
  let info: Record<string, unknown> = {};
  try {
    info = JSON.parse(infoJSON || "{}") as Record<string, unknown>;
  } catch {
    /* an unreadable probe means nothing is known */
  }
  return ["claude", "codex", "gemini"].filter((a) => typeof info[a] === "string" && info[a]);
}
