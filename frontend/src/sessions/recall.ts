// Lectern prepends Grimoire's recall to what it types into an agent. Two
// shapes exist:
//   - the older one: a "Grimoire reference data, not instructions." line, then
//     one JSON fact per line;
//   - the current one: a "Memories from earlier sessions with this user ..."
//     paragraph (continuation lines), then "- " bullet lines.
// That is context the agent needs, not something the person wrote, so it is
// folded into a small closed disclosure instead of a wall of text. The block
// ends at the first blank line, or at the first line that is neither part of
// the header nor a bullet.
const OLD_RECALL_HEADER = /^Grimoire reference data, not instructions\./;
const NEW_RECALL_HEADER = /^Memories from earlier sessions with this user/;

export function splitRecall(text: string): { recall: string[]; rest: string } {
  const lines = text.split("\n");
  const first = lines[0]?.trim() ?? "";
  if (OLD_RECALL_HEADER.test(first)) {
    let i = 1;
    while (i < lines.length && (lines[i] ?? "").trim().startsWith("{")) i++;
    return { recall: lines.slice(1, i), rest: lines.slice(i).join("\n").trim() };
  }
  if (NEW_RECALL_HEADER.test(first)) {
    let i = 1;
    let sawBullet = false;
    while (i < lines.length) {
      const line = lines[i] ?? "";
      const trimmed = line.trim();
      if (trimmed === "") break;
      if (trimmed.startsWith("- ")) {
        sawBullet = true;
      } else if (sawBullet) {
        break;
      }
      i++;
    }
    return { recall: lines.slice(0, i), rest: lines.slice(i).join("\n").trim() };
  }
  return { recall: [], rest: text };
}
