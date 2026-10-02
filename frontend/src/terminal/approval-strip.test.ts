import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import type { Approval } from "../types";
import { pendingFor } from "./ApprovalStrip";

const row = (id: number, session_id: number | null, status = "pending") => ({ id, session_id, status }) as unknown as Approval;

test("the terminal strip shows this session's pending approvals, oldest first", () => {
  const rows = [row(9, 3), row(4, 3), row(5, 7), row(6, 3, "approved"), row(8, null)];
  assert.deepEqual(pendingFor(rows, 3).map((r) => r.id), [4, 9]);
  assert.deepEqual(pendingFor(rows, 2), []);
});

test("a session's terminal renders the strip above the panes", () => {
  const app = readFileSync(join(import.meta.dirname, "App.tsx"), "utf8");
  const strip = app.indexOf("<ApprovalStrip");
  assert.ok(strip > 0 && strip < app.indexOf('<main id="workspace">'));
});
