// Pure decision logic behind the service worker's push/notification
// handlers (docs/agent-events.md section 3). Split out of service-worker.ts
// because that file imports `self` as a ServiceWorkerGlobalScope at module
// load time — merely importing it outside a real worker throws, so nothing
// in it is unit-testable directly. Everything here has no DOM/worker
// dependency and is exercised by sw-actions.test.ts; service-worker.ts wires
// it to the real push/notificationclick events.

/** The JSON payload a web-push message carries (internal/sinks.pushMessage). */
export interface PushData {
  title?: string;
  body?: string;
  url?: string;
  kind?: string;
  approval_id?: number;
  session_id?: number;
}

export interface NotificationPlan {
  title: string;
  options: {
    body: string;
    icon: string;
    badge: string;
    tag: string;
    renotify: boolean;
    vibrate: number[];
    data: { url: string; approvalId?: number; sessionId?: number };
    actions: { action: string; title: string }[];
  };
}

// groupTag scopes a notification to the session (or approval) it is about,
// so a new state for the same session REPLACES the old tray entry instead of
// stacking a second one under it — a stale "needs permission" sitting next
// to its own later "approved" confirmation is confusing, not informative.
// Anything with neither a session nor an approval id (a broadcast like
// "Check failed") falls back to its kind, which still groups repeats of the
// same broadcast rather than piling one notification per failure.
function groupTag(data: PushData): string {
  if (data.kind === "approval" && data.approval_id != null) return `approval-${data.approval_id}`;
  if (data.session_id != null) return `session-${data.session_id}`;
  return `kind-${data.kind || "general"}`;
}

// sessionActions offers a shortcut to the terminal and a quick in-app reply
// for the two kinds where a session is actually waiting on a person — not a
// decision, just their attention. Approval keeps its own Approve/Deny pair;
// Chromium caps visible notification actions at two, so these are only
// offered where there is no approval to decide.
function sessionActions(data: PushData): { action: string; title: string }[] {
  if (data.kind !== "waiting_permission" && data.kind !== "waiting_input") return [];
  if (data.session_id == null) return [];
  return [
    { action: "terminal", title: "⌨ Open terminal" },
    { action: "reply", title: "💬 Reply" },
  ];
}

// buildNotificationPlan turns one push payload into what showNotification
// needs. Only an "approval" push (one with an approval_id) gets Approve/Deny
// action buttons; a session waiting for permission or input gets Open
// terminal/Reply instead. Every other kind (idle, error, compacting) is
// informational and only opens the session on a body tap. Every notification
// is grouped and set to renotify, so a session's tray entry always reflects
// its latest state rather than accumulating one per event; approvals also
// get a distinct, more insistent vibration pattern.
export function buildNotificationPlan(data: PushData): NotificationPlan {
  const approvalId = data.kind === "approval" ? data.approval_id : undefined;
  return {
    title: data.title || "lectern",
    options: {
      body: data.body || "",
      icon: "/icon.svg",
      badge: "/icon.svg",
      tag: groupTag(data),
      renotify: true,
      vibrate: approvalId != null ? [200, 80, 200, 80, 200] : [120],
      data: { url: data.url || "/", approvalId, sessionId: data.session_id },
      actions:
        approvalId != null
          ? [
              { action: "approve", title: "✅ Approve" },
              { action: "deny", title: "⛔ Deny" },
            ]
          : sessionActions(data),
    },
  };
}

// actionURL builds the deep link for the "Open terminal"/"Reply" actions.
// It deliberately ignores the push payload's own `url` (a path like
// "/session/7" the server does not serve as the SPA — only "/" and its hash
// fragments resolve client-side) and instead builds a hash the app's own
// router already understands, so both actions always land on the real app
// regardless of what path the notification's default body tap would use.
export function actionURL(action: "terminal" | "reply", sessionId: number): string {
  return `/#session/${sessionId}/${action}`;
}

export type Decision = "approved" | "denied";

// decisionForAction maps a notification action id to the decision it means,
// or null for anything that is not a decision (the default body-tap click,
// whose event.action is "", or a future action this build does not know).
export function decisionForAction(action: string): Decision | null {
  if (action === "approve") return "approved";
  if (action === "deny") return "denied";
  return null;
}

export function decisionURL(approvalId: number): string {
  return `/api/approvals/${approvalId}/decision`;
}

// decisionRequestInit is a same-origin POST with no Authorization header —
// the phone is authenticated by Tailscale identity (or, in token mode, the
// lectern_token cookie), never by a bearer token a service worker would have
// to source from somewhere.
export function decisionRequestInit(decision: Decision): RequestInit {
  return {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({ decision }),
  };
}

// confirmationNotification is what the phone shows right after an in-tray
// decision — success or a failure like 403 (someone else already decided,
// or auth doesn't recognize this device) or the approval having expired.
export function confirmationNotification(
  decision: Decision,
  ok: boolean,
): { title: string; body: string } {
  if (!ok)
    return {
      title: decision === "approved" ? "Could not approve" : "Could not deny",
      body: "Open lectern to decide from the app instead.",
    };
  return { title: decision === "approved" ? "Approved" : "Denied", body: "Sent from the notification." };
}
