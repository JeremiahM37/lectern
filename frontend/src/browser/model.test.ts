import { test } from "node:test";
import assert from "node:assert/strict";
import {
  containedBox,
  fromFrame,
  loopbackPort,
  mapPoint,
  mergeSelection,
  normalizeAddress,
  readPicker,
  viewFramable,
  viewURL,
  type DesignElement,
} from "./model";

const el = (selector: string): DesignElement => ({
  selector, breadcrumb: selector, tag: "div", text: "", html: "<div></div>", html_truncated: false, html_length: 11,
  css: {}, rules: [], rect: { x: 0, y: 0, width: 10, height: 10 }, scroll: { x: 0, y: 0 },
  viewport: { width: 390, height: 844, dpr: 3 }, url: "http://x/", title: "",
});

test("a click on the letterboxed picture maps into page pixels", () => {
  // A 1280x800 page shown in a 640x800 box: 640x400, centred vertically.
  const box = { left: 100, top: 0, width: 640, height: 800 };
  assert.deepEqual(containedBox(box, 1280, 800), { left: 100, top: 200, width: 640, height: 400 });
  assert.deepEqual(mapPoint(100 + 320, 200 + 200, box, { width: 1280, height: 800 }), { x: 640, y: 400 });
  assert.equal(mapPoint(420, 100, box, { width: 1280, height: 800 }), null, "the letterbox is outside the page");
});

test("selection: click replaces, shift adds, shift on a picked element removes", () => {
  let list = mergeSelection([], el("#a"), false);
  list = mergeSelection(list, el("#b"), true);
  assert.deepEqual(list.map((x) => x.selector), ["#a", "#b"]);
  list = mergeSelection(list, el("#a"), true);
  assert.deepEqual(list.map((x) => x.selector), ["#b"]);
  assert.deepEqual(mergeSelection(list, el("#c"), false).map((x) => x.selector), ["#c"]);
  for (let i = 0; i < 12; i++) list = mergeSelection(list, el("#n" + i), true);
  assert.equal(list.length, 8);
});

test("only the picker's own messages from the framed view are read", () => {
  const frame = {}, origin = "http://host:19201";
  const good = { origin, source: frame, data: { lecternDesign: { type: "select", additive: true, element: el("#x") } } };
  assert.equal(fromFrame(good, frame, origin)?.type, "select");
  assert.equal(fromFrame({ ...good, origin: "http://evil:1" }, frame, origin), null);
  assert.equal(fromFrame({ ...good, source: {} }, frame, origin), null);
  assert.equal(fromFrame({ origin, source: frame, data: { lecternDesign: { type: "select", element: { selector: "" } } } }, frame, origin), null);
  assert.equal(readPicker({ type: "run", code: "x" }), null);
  const cap = readPicker({ type: "capture", id: "1", png: "javascript:alert(1)" });
  assert.equal(cap && cap.type === "capture" && cap.png, undefined, "a capture is only ever a PNG data URL");
});

test("a view is framed on this host, and never as http inside https", () => {
  const view = { id: "v", port: 5173, listen_port: 19201, design: false, tls: false, ticket: "t1" };
  assert.equal(viewURL({ protocol: "http:", hostname: "box" }, view, "/a?b=1"), "http://box:19201/a?b=1&__lectern_ticket=t1");
  assert.equal(viewFramable({ protocol: "https:" }, view), false);
  assert.equal(viewFramable({ protocol: "https:" }, { ...view, tls: true }), true);
  assert.match(viewURL({ protocol: "https:", hostname: "box.ts.net" }, { ...view, tls: true }), /^https:\/\/box\.ts\.net:19201\//);
});

test("addresses", () => {
  assert.equal(normalizeAddress("5173"), "http://localhost:5173/");
  assert.equal(normalizeAddress(":3000/x"), "http://localhost:3000/x");
  assert.equal(normalizeAddress("example.com"), "http://example.com");
  assert.equal(loopbackPort("http://127.0.0.1:8080/x"), 8080);
  assert.equal(loopbackPort("https://example.com/"), null);
});
