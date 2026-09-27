import assert from "node:assert/strict";
import { test } from "node:test";
import { existenceCheck } from "./linkCheck";
import { shellPath } from "../terminal/model";

test("a bare name is checked once, a miss is re-asked soon, a folder is not a file", async () => {
  const asked: string[] = [];
  let clock = 0;
  const files: Record<string, { exists: boolean; directory?: boolean }> = { "report.md": { exists: true }, src: { exists: true, directory: true } };
  const check = existenceCheck(
    {
      exists: async (path: string) => {
        asked.push(path);
        return { path, ...(files[path] || { exists: false }) };
      },
    },
    () => clock,
  );
  const first = check("report.md");
  assert.ok(first instanceof Promise);
  assert.equal(check("report.md"), first, "concurrent asks share one request");
  assert.equal(await first, true);
  assert.equal(check("report.md"), true, "remembered");
  assert.equal(await check("src"), false);
  assert.equal(await check("Jeremiah_Mackey_Cerebras.pdf"), false);
  assert.equal(check("Jeremiah_Mackey_Cerebras.pdf"), false, "a miss is remembered briefly");
  clock += 6000;
  files["Jeremiah_Mackey_Cerebras.pdf"] = { exists: true };
  assert.equal(await check("Jeremiah_Mackey_Cerebras.pdf"), true, "then asked again");
  assert.deepEqual(asked, ["report.md", "src", "Jeremiah_Mackey_Cerebras.pdf", "Jeremiah_Mackey_Cerebras.pdf"]);
});

test("paths handed to the agent are absolute shell words", () => {
  assert.equal(shellPath("/w/", "docs/a b.md"), "'/w/docs/a b.md'");
  assert.equal(shellPath("/w", "/home/admin/.formwork/x.pdf"), "'/home/admin/.formwork/x.pdf'");
  assert.equal(shellPath("/w", "~/notes/it's.md"), "~/'notes/it'\\''s.md'");
});
