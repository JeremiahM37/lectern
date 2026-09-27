// Markdown formatting commands for RichEditor, kept pure so they are unit
// tested (format.test.ts).

export type Format = "bold" | "italic" | "strike" | "code" | "link" | "heading" | "bullet" | "number" | "quote" | "codeblock";

export interface Edit {
  text: string;
  start: number;
  end: number;
}

const WRAP: Partial<Record<Format, [string, string, string]>> = {
  bold: ["**", "**", "bold text"],
  italic: ["*", "*", "italic text"],
  strike: ["~~", "~~", "struck text"],
  code: ["`", "`", "code"],
};

/** Applies a format to text[start:end] and returns the new text with the
 * selection to restore. Wrapping formats toggle off when the selection is
 * already wrapped; line formats apply to every selected line. */
export function applyFormat(text: string, start: number, end: number, f: Format): Edit {
  const sel = text.slice(start, end);
  const wrap = WRAP[f];
  if (wrap) {
    const [open, close, hint] = wrap;
    if (text.slice(start - open.length, start) === open && text.slice(end, end + close.length) === close) {
      return { text: text.slice(0, start - open.length) + sel + text.slice(end + close.length), start: start - open.length, end: end - open.length };
    }
    const inner = sel || hint;
    return { text: text.slice(0, start) + open + inner + close + text.slice(end), start: start + open.length, end: start + open.length + inner.length };
  }
  if (f === "link") {
    const label = sel || "link text";
    const out = `[${label}](https://)`;
    const urlAt = start + label.length + 3;
    return { text: text.slice(0, start) + out + text.slice(end), start: urlAt, end: urlAt + 8 };
  }
  if (f === "codeblock") {
    const before = start > 0 && text[start - 1] !== "\n" ? "\n" : "";
    const block = `${before}\`\`\`\n${sel || "code"}\n\`\`\`\n`;
    const at = start + before.length + 4;
    return { text: text.slice(0, start) + block + text.slice(end), start: at, end: at + (sel || "code").length };
  }
  // line formats: extend the selection to whole lines
  const ls = text.lastIndexOf("\n", start - 1) + 1;
  let le = text.indexOf("\n", end);
  if (le < 0) le = text.length;
  const lines = text.slice(ls, le).split("\n");
  const prefix = (i: number) => (f === "heading" ? "## " : f === "bullet" ? "- " : f === "number" ? `${i + 1}. ` : "> ");
  const pattern = f === "heading" ? /^#{1,6}\s/ : f === "bullet" ? /^[-*+]\s/ : f === "number" ? /^\d+[.)]\s/ : /^>\s?/;
  const all = lines.every((l) => pattern.test(l));
  const next = lines.map((l, i) => (all ? l.replace(pattern, "") : prefix(i) + l)).join("\n");
  return { text: text.slice(0, ls) + next + text.slice(le), start: ls, end: ls + next.length };
}
