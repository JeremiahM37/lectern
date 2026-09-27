import assert from "node:assert/strict";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import en from "./en";
import { pseudo, resolveLocale, setLocale, t } from "./index";

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sources(path);
    return /\.tsx?$/.test(name) && !name.endsWith(".test.ts") ? [path] : [];
  });
}

test("every key the app looks up has English text", () => {
  const root = join(import.meta.dirname, "..");
  const missing = new Set<string>();
  for (const file of sources(root)) {
    const text = readFileSync(file, "utf8");
    for (const match of text.matchAll(/\bt\(\s*"([a-zA-Z0-9_.]+)"/g)) {
      const key = match[1]!;
      if (key.endsWith(".") || key === "key") continue; // a prefix completed at runtime
      if (!(key in en)) missing.add(`${key} (${file.slice(root.length + 1)})`);
    }
  }
  assert.deepEqual([...missing], []);
});

test("placeholders fill in, and a missing key falls back sensibly", () => {
  setLocale("en");
  assert.equal(t("workspace.closeView", { label: "Build" }), "Close terminal view: Build");
  assert.equal(t("no.such.key", undefined, "Fallback"), "Fallback");
  assert.equal(t("no.such.key"), "no.such.key");
});

test("the pseudo-locale marks text but keeps placeholders working", () => {
  assert.equal(pseudo("Hi {name}"), "[Ĥí {name}]");
  setLocale("en-XA");
  assert.equal(t("workspace.closeView", { label: "Build" }), "[Çĺóšé ţéŕɱíñáĺ ṽíéŵ: Build]");
  setLocale("en");
});

test("an unknown language resolves to English, a regional one to its base", () => {
  assert.equal(resolveLocale("", ["xx-YY"]), "en");
  assert.equal(resolveLocale("en-GB"), "en");
  assert.equal(resolveLocale("en-XA"), "en-XA");
});
