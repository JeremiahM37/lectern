import test from "node:test";
import assert from "node:assert/strict";
import { parseCodeFromHash, suggestedDeviceName } from "./Pair";

test("parseCodeFromHash reads the code fragment", () => {
  assert.equal(parseCodeFromHash("#code=A1B2C3D4"), "A1B2C3D4");
  assert.equal(parseCodeFromHash("#code=A1B2%2DC3D4"), "A1B2-C3D4");
  assert.equal(parseCodeFromHash(""), "");
  assert.equal(parseCodeFromHash("#other=1"), "");
});

test("parseCodeFromHash stops at the next fragment param", () => {
  assert.equal(parseCodeFromHash("#code=ABCD&other=1"), "ABCD");
});

test("suggestedDeviceName recognizes common platforms", () => {
  assert.equal(
    suggestedDeviceName(
      "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15",
    ),
    "iPhone",
  );
  assert.equal(
    suggestedDeviceName("Mozilla/5.0 (Linux; Android 14; Pixel 8) Mobile Safari/537.36"),
    "Android phone",
  );
  assert.equal(
    suggestedDeviceName("Mozilla/5.0 (Linux; Android 14; SM-X200) Safari/537.36"),
    "Android tablet",
  );
  assert.equal(
    suggestedDeviceName("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)"),
    "Mac",
  );
  assert.equal(suggestedDeviceName(""), "My device");
});
