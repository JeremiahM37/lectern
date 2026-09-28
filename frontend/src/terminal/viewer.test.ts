import assert from "node:assert/strict";
import { test } from "node:test";
import { ApiError } from "../api";
import { setLocale } from "../i18n";
import { missingViewer } from "./viewer";

test("a missing terminal viewer becomes a notice naming the install command", () => {
  setLocale("en");
  const viewer = missingViewer(new ApiError(503, "Terminal viewer isn't installed — run: brew install ttyd",
    { code: "terminal_viewer_missing", fix: "brew install ttyd" }));
  assert.ok(viewer);
  assert.equal(viewer.message, "Terminal viewer isn't installed — run brew install ttyd");
  assert.equal(viewer.action.label, "Copy command");
});

test("any other failure is left to the usual handling", () => {
  assert.equal(missingViewer(new ApiError(503, "starting")), null);
  assert.equal(missingViewer(new ApiError(409, "ended", { code: "terminal_viewer_missing", fix: "x" })), null);
  assert.equal(missingViewer(new Error("network")), null);
});
