// Pure wording for a usage-limit hold (internal/limits, docs/rate-limits.md):
// the card chip, the banner line and which one-tap choices make sense. Kept
// free of React so limit-label.test.ts can check it directly.
import type { LimitHold } from "../types";

// clock renders an epoch in the viewer's own zone: "3:40pm" today,
// "Sat 9:00am" within a week, a date beyond that.
export function clock(epoch: number, now: number = Date.now() / 1000, locale?: string): string {
  const at = new Date(epoch * 1000);
  const time = at
    .toLocaleTimeString(locale, { hour: "numeric", minute: "2-digit" })
    .replace(/\s?([AP]M)$/i, (_, m: string) => m.toLowerCase());
  const today = new Date(now * 1000);
  if (at.toDateString() === today.toDateString()) return time;
  if (Math.abs(epoch - now) < 6 * 86400) return `${at.toLocaleDateString(locale, { weekday: "short" })} ${time}`;
  return `${at.toLocaleDateString(locale, { month: "short", day: "numeric" })} ${time}`;
}

// limitLabel is the one-line state: "Limit — resumes 3:40pm".
export function limitLabel(h: LimitHold, now: number = Date.now() / 1000, locale?: string): string {
  const reset = h.reset_at ? clock(h.reset_at, now, locale) : "";
  switch (h.state) {
    case "resuming":
      return h.to_account ? `Swapped to ${h.to_account} — resuming…` : "Limit reset — resuming…";
    case "swapping":
      return `Limit — swapping to ${h.to_account || "another account"}…`;
    case "swapped":
      return `Limit — continues on ${h.to_account || "another account"}`;
    case "handing_off":
      return `Limit — handing off${h.fallback ? ` to ${h.fallback}` : ""}…`;
    case "requeued":
      return `Limit — resumes ${h.due_at ? clock(h.due_at, now, locale) : reset || "after the reset"}`;
  }
  if (h.policy === "wait") {
    const due = h.due_at ? clock(h.due_at, now, locale) : reset;
    return due ? `Limit — resumes ${due}` : "Limit — resumes after the reset";
  }
  if (h.reset_at && h.reset_at <= now) return "Limit reset — ready to resume";
  return reset ? `Limit — resets ${reset}` : "Limit — reset time unknown";
}

export type LimitChoice = "wait" | "resume_now" | "handoff" | "swap" | "dismiss";

// limitChoices is what the card offers for a hold. Nothing while Lectern is
// in the middle of acting on it.
export function limitChoices(h: LimitHold, now: number = Date.now() / 1000): LimitChoice[] {
  if (h.state !== "waiting") return [];
  const out: LimitChoice[] = [];
  const reset = h.reset_at && h.reset_at <= now;
  if (h.swap_to) out.push("swap");
  if (reset) out.push("resume_now");
  else if (h.policy !== "wait") out.push("wait");
  out.push("handoff", "dismiss");
  return out;
}

export function choiceLabel(c: LimitChoice, h: LimitHold): string {
  switch (c) {
    case "wait":
      return "Resume at reset";
    case "resume_now":
      return "Resume now";
    case "handoff":
      return h.fallback ? `Hand off to ${h.fallback}` : "Hand off…";
    case "swap":
      return `Swap to ${h.swap_to?.label || "another account"}`;
    case "dismiss":
      return "Dismiss";
  }
}
