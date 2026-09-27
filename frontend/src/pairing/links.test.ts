import assert from "node:assert/strict";
import { test } from "node:test";
import { androidIntentLink, appPairLink } from "./links";

test("an https pairing link has an app twin with the same secret", () => {
  assert.equal(appPairLink("https://lectern.example:8443/relay-pair#p=eyJ2IjoxfQ"), "lectern://pair?p=eyJ2IjoxfQ");
  assert.equal(
    appPairLink("http://10.0.2.2:19210/pair#code=ABCD1234"),
    "lectern://pair?origin=http%3A%2F%2F10.0.2.2%3A19210&code=ABCD1234",
  );
  assert.equal(appPairLink("https://x.example/relay-pair"), undefined);
  assert.equal(appPairLink("https://x.example/settings"), undefined);
  assert.equal(appPairLink("not a link"), undefined);
});

test("the Chrome intent link names the app and falls back to the web page", () => {
  const link = "https://h.example/relay-pair#p=abc";
  assert.equal(
    androidIntentLink(link),
    "intent://pair?p=abc#Intent;scheme=lectern;package=io.github.jeremiahm37.lectern;S.browser_fallback_url=https%3A%2F%2Fh.example%2Frelay-pair%23p%3Dabc;end",
  );
});
