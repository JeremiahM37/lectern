// Pure wording for a usage-limit hold (internal/limits, docs/rate-limits.md):
// the card chip, the banner line and which one-tap choices make sense. Kept
// free of React so limit-label.test.ts can check it directly.
import type { LimitHold } from "../types";
import { t } from "../i18n";

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
      return h.to_account ? t("app.limit.swappedResuming", { account: h.to_account }) : t("app.limit.resetResuming");
    case "swapping":
      return t("app.limit.swapping", { account: h.to_account || t("app.limit.anotherAccount") });
    case "swapped":
      return t("app.limit.continuesOn", { account: h.to_account || t("app.limit.anotherAccount") });
    case "handing_off":
      return h.fallback ? t("app.limit.handingOffTo", { agent: h.fallback }) : t("app.limit.handingOff");
    case "requeued": {
      const when = h.due_at ? clock(h.due_at, now, locale) : reset;
      return when ? t("app.limit.resumes", { time: when }) : t("app.limit.resumesAfterReset");
    }
  }
  if (h.policy === "wait") {
    const due = h.due_at ? clock(h.due_at, now, locale) : reset;
    return due ? t("app.limit.resumes", { time: due }) : t("app.limit.resumesAfterReset");
  }
  if (h.reset_at && h.reset_at <= now) return t("app.limit.readyToResume");
  return reset ? t("app.limit.resets", { time: reset }) : t("app.limit.resetUnknown");
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
      return t("app.limit.resumeAtReset");
    case "resume_now":
      return t("app.limit.resumeNow");
    case "handoff":
      return h.fallback ? t("app.limit.handOffTo", { agent: h.fallback }) : t("app.limit.handOff");
    case "swap":
      return t("app.limit.swapTo", { account: h.swap_to?.label || t("app.limit.anotherAccount") });
    case "dismiss":
      return t("app.limit.dismiss");
  }
}
