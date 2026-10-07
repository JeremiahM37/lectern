import assert from "node:assert/strict";
import { test } from "node:test";
import { sheetCeiling } from "./sheet";

const anchor = (top: number, bottom: number) => ({ top, bottom }) as DOMRect;

test("a sheet stops below the button that opened it", () => {
  assert.equal(sheetCeiling({ top: 0, bottom: 800, left: 0, width: 400, anchor: anchor(40, 76) }), 84);
});

test("a button below the sheet (the key row's Tools) does not limit it", () => {
  assert.equal(sheetCeiling({ top: 0, bottom: 714, left: 0, width: 400, anchor: anchor(720, 760) }), 24);
});

test("a short visible area (keyboard up) gives the sheet nearly all of it", () => {
  assert.equal(sheetCeiling({ top: 35, bottom: 185, left: 0, width: 400, anchor: anchor(40, 76) }), 43);
});
