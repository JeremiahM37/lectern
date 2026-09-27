import test from "node:test";
import assert from "node:assert/strict";
import { parseAction } from "./action";
import { b64url } from "../relay/noise";

const frag = (a: unknown) => "#a=" + b64url(new TextEncoder().encode(JSON.stringify(a)));

test("a background action may only call this Lectern's own API", () => {
  const ok = { id: "n1", method: "POST", path: "/api/approvals/7/decision", body: { decision: "approved" } };
  assert.deepEqual(parseAction(frag(ok)), ok);
  for (const path of ["https://evil.example/api/x", "//evil.example/api/x", "/api/../pair/mint?x", "/term/1", "/api/a b"]) {
    assert.equal(parseAction(frag({ ...ok, path })), undefined, path);
  }
  assert.equal(parseAction(frag({ ...ok, method: "PATCHX" })), undefined);
  assert.equal(parseAction("#a=%%%"), undefined);
  assert.equal(parseAction(""), undefined);
});
