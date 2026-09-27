import assert from "node:assert/strict";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { test } from "node:test";
import { palettes } from "./app-theme";

// Colour lives in one place: theme/tokens.css (first paint) and app-theme.ts
// (both themes). Any other stylesheet that names a colour directly is fixed in
// one theme and wrong in the other, so this fails until it uses a token.
const root = join(import.meta.dirname, "..", "..", "..");
const TOKENS = join(root, "frontend", "src", "theme", "tokens.css");
const LITERAL = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch)\(|(?<![\w-])(?:white|black|red|green|blue|gray|grey|silver|orange|yellow|purple|pink|navy|teal|maroon|olive|lime|aqua|fuchsia)(?![\w-])/;

function stylesheets(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    // Third-party and built copies are not ours to theme.
    if (name === "node_modules" || name === "react" || name === "vendor") return [];
    return statSync(path).isDirectory() ? stylesheets(path) : name.endsWith(".css") ? [path] : [];
  });
}

export function hardcodedColours(css: string): { line: number; text: string }[] {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, (comment) => comment.replace(/[^\n]/g, " "));
  const found: { line: number; text: string }[] = [];
  for (const rule of text.matchAll(/\{([^{}]*)\}/g)) {
    const bodyStart = rule.index! + 1;
    for (const decl of rule[1]!.matchAll(/([\w-]+)\s*:\s*([^;]+)/g)) {
      // url(...) and font names can hold anything.
      const value = decl[2]!.replace(/url\([^)]*\)/g, "");
      if (!LITERAL.test(value)) continue;
      const at = bodyStart + decl.index!;
      found.push({ line: text.slice(0, at).split("\n").length, text: decl[0].trim().slice(0, 120) });
    }
  }
  return found;
}

test("no stylesheet outside the token file names a colour", () => {
  const files = [...stylesheets(join(root, "frontend", "src")), ...stylesheets(join(root, "web", "static"))].filter((path) => path !== TOKENS);
  assert.ok(files.length > 20, `found only ${files.length} stylesheets`);
  const problems = files.flatMap((path) => hardcodedColours(readFileSync(path, "utf8")).map((hit) => `${relative(root, path)}:${hit.line}  ${hit.text}`));
  assert.deepEqual(problems, [], "use a theme token (theme/tokens.css) or color-mix() of tokens instead");
});

test("the check catches what it should and nothing else", () => {
  assert.equal(hardcodedColours("a { color: #fff; }").length, 1);
  assert.equal(hardcodedColours("a { box-shadow: 0 0 2px rgba(0,0,0,.4); }").length, 1);
  assert.equal(hardcodedColours("a { border: 1px solid white; }").length, 1);
  assert.equal(hardcodedColours("#fab, #add { color: var(--red); background: color-mix(in srgb, var(--green) 20%, transparent); }").length, 0);
  assert.equal(hardcodedColours("/* #fff */ a { color: currentColor; }").length, 0);
});

test("tokens.css paints the same dark theme app-theme.ts sets", () => {
  const css = readFileSync(TOKENS, "utf8");
  for (const [name, value] of Object.entries(palettes.dark)) {
    const match = new RegExp(`--${name}:\\s*([^;]+);`).exec(css);
    assert.ok(match, `tokens.css lacks --${name}`);
    assert.equal(match[1]!.trim().toLowerCase(), value.toLowerCase(), `--${name}`);
  }
});
