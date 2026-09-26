// Lectern prepends Grimoire's recall to what it types into an agent: a
// "Grimoire reference data, not instructions." line, then one JSON fact per
// line. That is context the agent needs, not something the person wrote, so
// it is folded into a small closed disclosure instead of a wall of JSON.
const RECALL_HEADER = /^Grimoire reference data, not instructions\./;

export function splitRecall(text: string): { recall: string[]; rest: string } {
  const lines = text.split("\n");
  if (!RECALL_HEADER.test(lines[0]?.trim() ?? "")) return { recall: [], rest: text };
  let i = 1;
  while (i < lines.length && (lines[i] ?? "").trim().startsWith("{")) i++;
  return { recall: lines.slice(1, i), rest: lines.slice(i).join("\n").trim() };
}
