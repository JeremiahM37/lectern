import assert from "node:assert/strict";
import { test } from "node:test";
import { ApiError } from "../api";
import { NeedsHistory, closedAge, reopenSession } from "./restore";
import type { SessionsApi } from "./Sessions";

function api(respond: (path: string, body: unknown) => unknown): SessionsApi {
  return {
    sessions: async () => [],
    request: async <T,>(path: string, options?: { body?: unknown }) =>
      respond(path, options?.body) as T,
  } as SessionsApi;
}

test("reopen posts the choice to the record's reopen endpoint", async () => {
  let seen: [string, unknown] | undefined;
  const result = await reopenSession(
    api((path, body) => {
      seen = [path, body];
      return { session: { id: 9 }, source_id: 4, action: "continue", message: "ok" };
    }),
    4,
    { agent: "codex", model: "gpt" },
  );
  assert.deepEqual(seen, ["/sessions/4/reopen", { agent: "codex", model: "gpt" }]);
  assert.equal(result.session.id, 9);
});

test("a record without a bound conversation asks for the history picker", async () => {
  const failing = api(() => {
    throw new ApiError(409, "no conversation is bound", { detail: "no conversation is bound", needs_history: true });
  });
  await assert.rejects(reopenSession(failing, 3), NeedsHistory);
  const other = api(() => {
    throw new ApiError(409, "the original terminal is still running", { detail: "x" });
  });
  await assert.rejects(reopenSession(other, 3), (error) => !(error instanceof NeedsHistory));
});

test("ages read naturally", () => {
  const now = Date.now() / 1000;
  assert.equal(closedAge(now - 5), "just now");
  assert.equal(closedAge(now - 125), "2m ago");
  assert.equal(closedAge(now - 3 * 3600 - 60), "3h 1m ago");
  assert.equal(closedAge(null), "recently");
});
