// What a Scribe question carries and the exact answer text each choice sends
// (internal/scribe/lecternbridge and internal/api/hooks_agentevents.go). A
// question arrives as an approval whose tool_name is "AskUser"; the answer goes
// back through the ordinary decision as "approved" with the answer as its note.
import type { Approval } from "../types";

export const ASK_USER_TOOL = "AskUser";

// The plan-review answers Scribe matches on (internal/scribe/agent/interactive.go).
// They travel verbatim as the answer, so they are never translated; only the
// button labels are.
export const PLAN_ANSWER = {
  execute: "Execute",
  clear: "Clear context and execute",
  revise: "Revise",
  cancel: "Cancel",
} as const;

export interface AskUserView {
  kind: "question" | "plan_review";
  question: string;
  options: string[];
  body: string;
  allowFreeText: boolean;
}

export function isAskUser(approval: Pick<Approval, "tool_name">): boolean {
  return approval.tool_name === ASK_USER_TOOL;
}

/** The question as the card shows it. A question with no options always gets a free-text box. */
export function askUserView(input: Record<string, unknown>): AskUserView {
  const options = Array.isArray(input.options)
    ? input.options.filter((o): o is string => typeof o === "string" && o.trim() !== "")
    : [];
  return {
    kind: input.kind === "plan_review" ? "plan_review" : "question",
    question: typeof input.question === "string" ? input.question : "",
    options,
    body: typeof input.body === "string" ? input.body : "",
    allowFreeText: input.allow_free_text === true || options.length === 0,
  };
}

/** Scribe reads everything after the word "Revise" as the feedback, so a blank box sends the bare word. */
export function reviseAnswer(feedback: string): string {
  const text = feedback.trim();
  return text ? `${PLAN_ANSWER.revise} ${text}` : PLAN_ANSWER.revise;
}
