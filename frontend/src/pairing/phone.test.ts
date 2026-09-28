import assert from "node:assert/strict";
import { test } from "node:test";
import { bestChoice, directPairURL, isLoopbackURL, phoneChoices, type PhoneAddresses } from "./phone";

const local: PhoneAddresses = {
  listening: "127.0.0.1:41429",
  loopback_only: true,
  options: [
    { kind: "tailnet", url: "http://box.tail1.ts.net:41429", available: false, reason: "loopback_only" },
    { kind: "lan", url: "http://192.168.0.75:41429", available: false, reason: "loopback_only" },
    { kind: "relay", available: false, reason: "relay_not_set_up" },
  ],
};

test("a loopback address is never offered, and never becomes a QR code", () => {
  for (const url of ["http://127.0.0.1:9110", "http://localhost:1", "http://[::1]:2", "http://0.0.0.0:3"]) assert.ok(isLoopbackURL(url), url);
  assert.ok(!isLoopbackURL("http://192.168.0.75:9110"));
  // The browser on this computer's loopback adds no "current address" choice.
  const choices = phoneChoices(local, "http://127.0.0.1:41429");
  assert.deepEqual(choices.map((row) => row.kind), ["tailnet", "lan", "relay"]);
  assert.equal(bestChoice(choices), undefined);
  // Even a server that did offer loopback is overruled.
  const odd = phoneChoices({ ...local, options: [{ kind: "lan", url: "http://127.0.0.1:1", available: true }] }, "");
  assert.equal(odd[0]!.available, false);
});

test("the address in use comes first when a phone could use it too", () => {
  const served: PhoneAddresses = { ...local, loopback_only: false, options: [{ kind: "tailnet", url: "https://box.tail1.ts.net:8443", available: true, secure: true }, { kind: "lan", url: "http://192.168.0.75:9110", available: true }] };
  const choices = phoneChoices(served, "https://box.tail1.ts.net:8443");
  assert.deepEqual(choices.map((row) => row.kind), ["current", "lan"]);
  assert.equal(bestChoice(choices)!.url, "https://box.tail1.ts.net:8443");
  assert.equal(directPairURL("https://box.tail1.ts.net:8443/", "ab cd"), "https://box.tail1.ts.net:8443/pair#code=ab%20cd");
});
