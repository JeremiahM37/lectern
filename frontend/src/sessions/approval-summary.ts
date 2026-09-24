import type { Approval } from "../types";

// approvalSummary renders a tool call's input down to the one line a person
// glances at before deciding — shared by NeedsYou's list row and the
// SessionCard banner (docs/agent-events.md section 3) so both read the same
// fields the same way.
export function approvalSummary(approval: Approval): string {
  const input = approval.input || {};
  for (const field of ["command", "path", "file_path", "url", "pattern"]) {
    const value = input[field];
    if (typeof value === "string" && value.trim()) return value.trim().slice(0, 90);
  }
  const text = JSON.stringify(input);
  return text && text !== "{}" ? text.slice(0, 90) : "";
}
