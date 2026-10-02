import assert from "node:assert/strict";
import { test } from "node:test";
import { tmuxOnPath, type OnboardingStatus } from "./onboarding";

const status = (extra: Partial<OnboardingStatus> = {}): OnboardingStatus =>
  ({ agents: [], git: { ok: true }, tmux: { ok: true, detail: "/usr/bin/tmux" }, ...extra });

test("the tmux link shows only where the server says tmux is installed", () => {
  assert.equal(tmuxOnPath(undefined), false);
  assert.equal(tmuxOnPath(status({ tmux_installed: true })), true);
  assert.equal(tmuxOnPath(status({ tmux_installed: false })), false);
  // tmux.ok alone is not enough: the PTY host makes it true without tmux.
  assert.equal(tmuxOnPath(status()), false);
});
