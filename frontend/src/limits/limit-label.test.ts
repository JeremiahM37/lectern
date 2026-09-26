import assert from "node:assert/strict";
import { test } from "node:test";
import { choiceLabel, limitChoices, limitLabel } from "./limit-label";
import type { LimitHold } from "../types";

process.env.TZ = "UTC";

function hold(over: Partial<LimitHold>): LimitHold {
  return {
    id: 1, agent: "claude", source: "pane", pattern: "claude-limit",
    message: "You've hit your session limit · resets 3:40pm (UTC)",
    detected_at: 0, policy: "notify", state: "waiting", tries: 0, policy_scope: "default",
    ...over,
  };
}

// 2026-09-26 12:00 UTC
const now = Date.UTC(2026, 8, 26, 12, 0) / 1000;
const at1540 = Date.UTC(2026, 8, 26, 15, 40) / 1000;

test("the card says when the limit resets and what Lectern will do", () => {
  assert.equal(limitLabel(hold({ reset_at: at1540 }), now, "en-US"), "Limit — resets 3:40pm");
  assert.equal(
    limitLabel(hold({ reset_at: at1540, policy: "wait", due_at: at1540 + 60 }), now, "en-US"),
    "Limit — resumes 3:41pm",
  );
  assert.equal(limitLabel(hold({}), now, "en-US"), "Limit — reset time unknown");
  assert.equal(limitLabel(hold({ reset_at: now - 60 }), now, "en-US"), "Limit reset — ready to resume");
  assert.equal(limitLabel(hold({ state: "resuming" }), now, "en-US"), "Limit reset — resuming…");
  assert.equal(
    limitLabel(hold({ state: "handing_off", fallback: "codex" }), now, "en-US"),
    "Limit — handing off to codex…",
  );
  assert.equal(
    limitLabel(hold({ state: "requeued", due_at: at1540 + 90 }), now, "en-US"),
    "Limit — resumes 3:41pm",
  );
  assert.match(limitLabel(hold({ reset_at: now + 3 * 86400 }), now, "en-US"), /^Limit — resets Tue 12:00pm$/);
});

test("one-tap choices follow the hold's state and policy", () => {
  assert.deepEqual(limitChoices(hold({ reset_at: at1540 }), now), ["wait", "handoff", "dismiss"]);
  assert.deepEqual(limitChoices(hold({ reset_at: at1540, policy: "wait" }), now), ["handoff", "dismiss"]);
  assert.deepEqual(limitChoices(hold({ reset_at: now - 1 }), now), ["resume_now", "handoff", "dismiss"]);
  assert.deepEqual(limitChoices(hold({ state: "resuming" }), now), []);
  assert.equal(choiceLabel("handoff", hold({ fallback: "codex · gpt-5" })), "Hand off to codex · gpt-5");
  assert.equal(choiceLabel("handoff", hold({})), "Hand off…");
});
