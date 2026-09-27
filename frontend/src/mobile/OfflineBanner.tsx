// "Offline · showing what Lectern said 4 min ago", over any screen whose
// data came from this device's cache (api/offline.ts) rather than from
// Lectern just now. It goes away by itself when a request gets through.
import { t } from "../i18n";
import { useEffect, useState } from "react";
import { offlineCache, staleAge, type OfflineState } from "../api/offline";

export function OfflineBanner({ onRetry }: { onRetry: () => void }) {
  const [state, setState] = useState<OfflineState>(offlineCache.state);
  const [, tick] = useState(0);
  useEffect(() => {
    const change = () => setState(offlineCache.state);
    // A request that finished after the cached answer was shown: fetch
    // everything again so every screen shows the live answer.
    const revalidated = () => onRetry();
    offlineCache.addEventListener("change", change);
    offlineCache.addEventListener("revalidated", revalidated);
    const timer = window.setInterval(() => tick((n) => n + 1), 30000);
    const online = () => onRetry();
    window.addEventListener("online", online);
    return () => {
      offlineCache.removeEventListener("change", change);
      offlineCache.removeEventListener("revalidated", revalidated);
      window.removeEventListener("online", online);
      clearInterval(timer);
    };
  }, [onRetry]);
  if (!state.stale) return null;
  return (
    <div id="offline-banner" role="status" data-stale-since={state.since}>
      <span className="offline-dot" aria-hidden="true" />
      <span>
        <b>{t("offline.title")}</b> · {t("offline.showing", { age: staleAge(state.since) })}
      </span>
      <button className="b" onClick={onRetry}>
        {t("offline.retry")}
      </button>
    </div>
  );
}
