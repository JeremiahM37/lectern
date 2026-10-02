// The chat's live output is the agent's terminal screen, which ends in the
// blank rows below its cursor. Rendered as is, those rows push the last real
// line out of view after every turn or approval, and the output looks empty
// (re-audit N6). Trim them so the newest text is what the view anchors to.
export function readerText(text: string): string {
  return text.replace(/\r/g, "").replace(/(?:[ \t ]*\n)+[ \t ]*$/, "").replace(/[ \t ]+$/, "");
}
