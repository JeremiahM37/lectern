"use strict";
(() => {
  // src/sw-actions.ts
  function groupTag(data) {
    if (data.kind === "approval" && data.approval_id != null) return `approval-${data.approval_id}`;
    if (data.session_id != null) return `session-${data.session_id}`;
    return `kind-${data.kind || "general"}`;
  }
  function sessionActions(data) {
    if (data.kind !== "waiting_permission" && data.kind !== "waiting_input") return [];
    if (data.session_id == null) return [];
    return [
      { action: "terminal", title: "\u2328 Open terminal" },
      { action: "reply", title: "\u{1F4AC} Reply" }
    ];
  }
  function buildNotificationPlan(data) {
    const approvalId = data.kind === "approval" ? data.approval_id : void 0;
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
        actions: approvalId != null ? [
          { action: "approve", title: "\u2705 Approve" },
          { action: "deny", title: "\u26D4 Deny" }
        ] : sessionActions(data)
      }
    };
  }
  function actionURL(action, sessionId) {
    return `/#session/${sessionId}/${action}`;
  }
  function decisionForAction(action) {
    if (action === "approve") return "approved";
    if (action === "deny") return "denied";
    return null;
  }
  function decisionURL(approvalId) {
    return `/api/approvals/${approvalId}/decision`;
  }
  function decisionRequestInit(decision) {
    return {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({ decision })
    };
  }
  function confirmationNotification(decision, ok) {
    if (!ok)
      return {
        title: decision === "approved" ? "Could not approve" : "Could not deny",
        body: "Open lectern to decide from the app instead."
      };
    return { title: decision === "approved" ? "Approved" : "Denied", body: "Sent from the notification." };
  }

  // src/badge.ts
  function applyBadge(nav, count) {
    try {
      if (count > 0) void nav.setAppBadge?.(count)?.catch(() => {
      });
      else void nav.clearAppBadge?.()?.catch(() => {
      });
    } catch {
    }
  }
  var ACTIONABLE_KINDS = /* @__PURE__ */ new Set(["approval", "waiting_permission", "waiting_input"]);
  function needsBadge(kind) {
    return !!kind && ACTIONABLE_KINDS.has(kind);
  }

  // src/service-worker.ts
  var worker = self;
  var CACHE = "lectern-react-508d47decfc2";
  worker.addEventListener("install", (event) => event.waitUntil((async () => {
    await (await caches.open(CACHE)).addAll(["/","/icon.svg","/manifest.webmanifest","/fonts.css","/fonts/inter-latin.woff2","/fonts/inter-latin-ext.woff2","/react/assets/app-CWn5ALlr.css","/react/assets/app-DDPymJWJ.js","/react/assets/terminal-Banmt6Cj.js","/react/assets/terminal-BsW0wNtV.css","/react/assets/viewport-DR5PCwew.css","/react/assets/viewport-HNwDtZFI.js"]);
    await worker.skipWaiting();
  })()));
  worker.addEventListener("activate", (event) => event.waitUntil((async () => {
    await Promise.all((await caches.keys()).filter((key) => key.startsWith("lectern-") && key !== CACHE).map((key) => caches.delete(key)));
    await worker.clients.claim();
  })()));
  worker.addEventListener("fetch", (event) => {
    const request = event.request, url = new URL(request.url);
    if (request.method !== "GET" || url.origin !== worker.location.origin || /^\/(api|term|terminal)\//.test(url.pathname)) return;
    const immutable = url.pathname.startsWith("/react/assets/") || url.pathname.startsWith("/fonts/") || url.pathname === "/icon.svg";
    event.respondWith((async () => {
      const cache = await caches.open(CACHE);
      if (immutable) {
        const cached = await cache.match(request);
        if (cached) return cached;
      }
      try {
        const response = await fetch(request);
        if (response.ok) await cache.put(request, response.clone());
        return response;
      } catch {
        return await cache.match(request) || (request.mode === "navigate" ? await cache.match("/") : void 0) || new Response("Lectern is offline", { status: 503 });
      }
    })());
  });
  var badgeCount = 0;
  worker.addEventListener("message", (event) => {
    const data = event.data;
    if (data?.type === "lec-badge-count" && typeof data.count === "number") badgeCount = Math.max(0, data.count);
  });
  worker.addEventListener("push", (event) => {
    let data = {};
    try {
      data = event.data?.json() || {};
    } catch {
    }
    const plan = buildNotificationPlan(data);
    if (needsBadge(data.kind)) {
      badgeCount += 1;
      applyBadge(navigator, badgeCount);
    }
    event.waitUntil(worker.registration.showNotification(plan.title, plan.options));
  });
  function resolveAppURL(raw) {
    const url = new URL(raw || "/", worker.location.origin);
    return url.origin === worker.location.origin ? url : new URL("/", worker.location.origin);
  }
  async function openApp(url) {
    const windows = await worker.clients.matchAll({ type: "window", includeUncontrolled: true });
    const app = windows[0];
    if (app) {
      await app.navigate(url.href);
      await app.focus();
    } else await worker.clients.openWindow(url.href);
  }
  worker.addEventListener("notificationclick", (event) => {
    const data = event.notification.data;
    const decision = decisionForAction(event.action);
    event.notification.close();
    if (decision && data?.approvalId != null) {
      const approvalId = data.approvalId;
      event.waitUntil((async () => {
        let ok = false;
        try {
          const resp = await fetch(decisionURL(approvalId), decisionRequestInit(decision));
          ok = resp.ok;
        } catch {
          ok = false;
        }
        const confirmation = confirmationNotification(decision, ok);
        await worker.registration.showNotification(confirmation.title, { body: confirmation.body, icon: "/icon.svg", badge: "/icon.svg", data: { url: resolveAppURL(data?.url).href } });
      })());
      return;
    }
    if ((event.action === "terminal" || event.action === "reply") && data?.sessionId != null) {
      event.waitUntil(openApp(resolveAppURL(actionURL(event.action, data.sessionId))));
      return;
    }
    event.waitUntil(openApp(resolveAppURL(data?.url)));
  });
})();
