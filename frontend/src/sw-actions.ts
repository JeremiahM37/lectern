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
    data: { url: string; approvalId?: number };
    actions: { action: string; title: string }[];
  };
}

// buildNotificationPlan turns one push payload into what showNotification
// needs. Only an "approval" push (one with an approval_id) gets Approve/Deny
// action buttons — every other kind (waiting_permission, waiting_input,
// idle, error, compacting) is informational and only opens the session on a
// body tap.
export function buildNotificationPlan(data: PushData): NotificationPlan {
  const approvalId = data.kind === "approval" ? data.approval_id : undefined;
  return {
    title: data.title || "lectern",
    options: {
      body: data.body || "",
      icon: "/icon.svg",
      badge: "/icon.svg",
      data: { url: data.url || "/", approvalId },
      actions:
        approvalId != null
          ? [
              { action: "approve", title: "✅ Approve" },
              { action: "deny", title: "⛔ Deny" },
            ]
          : [],
    },
  };
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
