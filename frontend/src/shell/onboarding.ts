// Reading GET /api/onboarding: what the first-run card needs to know.

export interface EnvCheck {
  ok: boolean;
  detail?: string;
  fix?: string;
  /** Newer servers: tmux is on PATH (whether or not sessions use it). */
  available?: boolean;
}
export interface OnboardingStatus {
  agents: { name: string; found: boolean }[];
  tmux: EnvCheck;
  git: EnvCheck;
  python?: EnvCheck;
  /** Newer servers may say this at the top level instead. */
  tmux_available?: boolean;
}

/**
 * Whether tmux is installed on the server's machine, so offering "Agents
 * already running in tmux? Find them" makes sense. A server that says so
 * outright is believed. An older one only reports whether sessions have a
 * keeper: with tmux as the backend, `detail` is tmux's path; with the
 * built-in PTY host it says "not needed", naming tmux's path only when tmux
 * is installed.
 */
export function tmuxOnPath(status: OnboardingStatus | undefined): boolean {
  if (!status) return false;
  if (typeof status.tmux_available === "boolean") return status.tmux_available;
  const tmux = status.tmux;
  if (!tmux) return false;
  if (typeof tmux.available === "boolean") return tmux.available;
  const detail = tmux.detail || "";
  if (/tmux at \S+/.test(detail)) return true;
  // A path (Unix or Windows) is where tmux was found.
  return tmux.ok && /^(\/|[A-Za-z]:\\)/.test(detail.trim());
}
