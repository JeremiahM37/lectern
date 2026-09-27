// What a swipe on a session card does (mobile/SwipeRow.tsx). Right approves
// the one approval the session is waiting on; left puts the record away the
// same way the card's own buttons do, with the same Undo toast.
import { t } from "../i18n";
import type { Approval, SessionView } from "../types";
import type { SwipeAction } from "./SwipeRow";

interface Deps {
  decide: (id: number) => Promise<unknown>;
  request: (path: string, init: { method: string; body?: { [key: string]: boolean } }) => Promise<unknown>;
  closed: (session: SessionView, text: string) => void;
  refresh: () => Promise<unknown>;
  notice: (text: string, error?: boolean) => void;
  confirm: (text: string) => boolean;
}

export function sessionSwipes(s: SessionView, approval: Approval | undefined, deps: Deps): { left?: SwipeAction; right?: SwipeAction } {
  const name = s.name || t("sessions.card.sessionFallback");
  const run = async (work: () => Promise<unknown>, text: string, undo = true) => {
    try {
      await work();
      if (undo) deps.closed(s, text);
      else deps.notice(text);
      await deps.refresh();
    } catch (error) {
      deps.notice(String(error), true);
    }
  };
  const right: SwipeAction | undefined =
    approval && approval.status === "pending"
      ? { label: t("swipe.approve"), tone: "ok", run: () => run(() => deps.decide(approval.id), t("swipe.approved", { tool: approval.tool_name, name }), false) }
      : undefined;
  let left: SwipeAction | undefined;
  if (s.archived_at != null || s.setup_state === "creating") left = undefined;
  else if (s.ended_at != null)
    left = { label: t("swipe.archive"), tone: "warn", run: () => run(() => deps.request(`/sessions/${s.id}/archive`, { method: "POST", body: { stop: false } }), t("swipe.archived", { name })) };
  else if (s.status === "dead")
    left = { label: t("swipe.dismiss"), tone: "warn", run: () => run(() => deps.request(`/sessions/${s.id}`, { method: "DELETE" }), t("swipe.dismissed", { name }), false) };
  else
    left = {
      label: t("swipe.archive"),
      tone: "warn",
      run: () =>
        deps.confirm(t("swipe.confirmStop", { name }))
          ? run(() => deps.request(`/sessions/${s.id}/archive`, { method: "POST", body: { stop: true } }), t("swipe.stopped", { name }))
          : undefined,
    };
  return { left, right };
}
