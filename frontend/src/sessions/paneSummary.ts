// What a phone card shows of a pane: the last lines that say what the agent
// is doing, without the agent's own furniture beneath them.
//
// The bottom of a Claude Code or Codex screen is always the same chrome: a
// rule, the prompt box, a status line and key hints. A desk card is tall
// enough to show it and what is above; a phone card has three lines, and
// they were all chrome.

const CHROME: RegExp[] = [
  /^[─━—-]{6,}$/, // the rules around the prompt box
  /^[❯›>]\s*$/, // an empty prompt
  /^›\s+Ask Codex to do anything/,
  /bypass permissions|shift\+tab to cycle|for shortcuts|← for agents|ctrl\+r to search|esc to interrupt/i,
  /^Tip:/,
  /new task\? \/clear/,
  /\(\d+[KM] context\)|\b\d+% ctx\b/, // Claude Code's status line
  /^(GPT|gpt|o\d|codex)[\w.-]* (low|medium|high|minimal)\b.*·/, // Codex's status line
  /^✔ Update installed/,
  /^\? for shortcuts/,
];

/** The pane with trailing agent chrome removed and long indents trimmed. */
export function paneSummary(tail: string): string {
  const lines = tail.replace(/\u00a0/g, " ").split("\n").map((line) => line.replace(/\s+$/, ""));
  while (lines.length) {
    const line = (lines[lines.length - 1] ?? "").trim();
    if (line === "" || CHROME.some((pattern) => pattern.test(line))) lines.pop();
    else break;
  }
  // Right-aligned notices are indented by most of a desk terminal's width;
  // what is left loses the indent every line shares.
  const kept = lines.map((line) => line.replace(/^\s{8,}/, "  "));
  const indent = Math.min(...kept.filter((line) => line.trim()).map((line) => line.length - line.trimStart().length));
  return kept.map((line) => line.slice(Number.isFinite(indent) ? indent : 0)).join("\n");
}
