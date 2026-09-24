"use strict";
(() => {
  // src/sw-actions.ts
  function buildNotificationPlan(data) {
    const approvalId = data.kind === "approval" ? data.approval_id : void 0;
    return {
      title: data.title || "lectern",
      options: {
        body: data.body || "",
        icon: "/icon.svg",
        badge: "/icon.svg",
        data: { url: data.url || "/", approvalId },
        actions: approvalId != null ? [
          { action: "approve", title: "\u2705 Approve" },
          { action: "deny", title: "\u26D4 Deny" }
        ] : []
      }
    };
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

  // src/service-worker.ts
  var worker = self;
  var CACHE = "lectern-react-fe5551df8bd0";
  worker.addEventListener("install", (event) => event.waitUntil((async () => {
    await (await caches.open(CACHE)).addAll(["/","/icon.svg","/manifest.webmanifest","/fonts.css","/fonts/inter-latin.woff2","/fonts/inter-latin-ext.woff2","/react/assets/app-8p7JzGT1.css","/react/assets/app-BZCKYl0S.js","/react/assets/terminal--ueVzdSu.js","/react/assets/terminal-CewVVPtL.css","/react/assets/viewport-DR5PCwew.css","/react/assets/viewport-HNwDtZFI.js"]);
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
  worker.addEventListener("push", (event) => {
    let data = {};
    try {
      data = event.data?.json() || {};
    } catch {
    }
    const plan = buildNotificationPlan(data);
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
    event.waitUntil(openApp(resolveAppURL(data?.url)));
  });
})();
