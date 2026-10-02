// Reading GET /api/onboarding: what the first-run card needs to know.

export interface EnvCheck {
  ok: boolean;
  detail?: string;
  fix?: string;
}
export interface OnboardingStatus {
  agents: { name: string; found: boolean }[];
  tmux: EnvCheck;
  git: EnvCheck;
  python?: EnvCheck;
  /** tmux is on the server's PATH, whether or not sessions use it. */
  tmux_installed?: boolean;
}

/**
 * Whether to offer "Agents already running in tmux? Find them": only where
 * the server says tmux is installed. `tmux.ok` is no guide — it is also true
 * when the built-in PTY host keeps sessions and tmux is absent. A server too
 * old to say is treated as "no", so a new user is never sent looking.
 */
export function tmuxOnPath(status: OnboardingStatus | undefined): boolean {
  return status?.tmux_installed === true;
}
