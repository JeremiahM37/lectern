import assert from "node:assert/strict";
import { test } from "node:test";
import { tmuxOnPath, type OnboardingStatus } from "./onboarding";

const status = (tmux: OnboardingStatus["tmux"], extra: Partial<OnboardingStatus> = {}): OnboardingStatus =>
  ({ agents: [], git: { ok: true }, tmux, ...extra });

test("the tmux link shows only where tmux is installed", () => {
  assert.equal(tmuxOnPath(undefined), false);
  // tmux backend: the check's detail is tmux's path.
  assert.equal(tmuxOnPath(status({ ok: true, detail: "/usr/bin/tmux" })), true);
  // Built-in PTY host, no tmux on PATH.
  assert.equal(tmuxOnPath(status({ ok: true, detail: "not needed: sessions are kept by lectern's built-in PTY host, and lectern attach draws its own key bar" })), false);
  // Built-in PTY host, tmux installed too.
  assert.equal(tmuxOnPath(status({ ok: true, detail: "not needed: … (tmux at /usr/bin/tmux is used only when LECTERN_SESSION_BACKEND=tmux)" })), true);
  assert.equal(tmuxOnPath(status({ ok: false, detail: "not found on PATH", fix: "install tmux" })), false);
  // A server that says so outright is believed.
  assert.equal(tmuxOnPath(status({ ok: true, detail: "/usr/bin/tmux", available: false })), false);
  assert.equal(tmuxOnPath(status({ ok: true, detail: "not needed" }, { tmux_available: true })), true);
});
