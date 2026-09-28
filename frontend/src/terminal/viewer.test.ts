import assert from "node:assert/strict";
import { test } from "node:test";
import { ApiError } from "../api";
import { setLocale } from "../i18n";
import { viewerUnavailable } from "./viewer";

test("a terminal server that cannot start becomes one notice saying what helps", () => {
  setLocale("en");
  const message = viewerUnavailable(new ApiError(503, "The web terminal could not start (exec failed)",
    { code: "terminal_viewer_unavailable", reason: "exec failed" }));
  assert.equal(message, "The web terminal could not start (exec failed). Restart Lectern: lectern local stop, then lectern up.");
});

test("any other failure is left to the usual handling", () => {
  assert.equal(viewerUnavailable(new ApiError(503, "starting")), null);
  assert.equal(viewerUnavailable(new ApiError(409, "ended", { code: "terminal_viewer_unavailable" })), null);
  assert.equal(viewerUnavailable(new Error("network")), null);
});
