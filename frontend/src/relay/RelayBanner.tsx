import { useEffect, useState } from "react";
import type { RelayTunnel } from "./tunnel";
import { t, useLocale } from "../i18n";

// A thin bar that appears only when the encrypted relay is not connected, so
// a phone that cannot reach Lectern says why instead of showing stale data.
export function RelayBanner({ tunnel }: { tunnel?: RelayTunnel }) {
  useLocale();
  const [, rerender] = useState(0);
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    if (!tunnel) return;
    let timer = 0;
    const on = () => {
      rerender((n) => n + 1);
      window.clearTimeout(timer);
      setSlow(false);
      // Brief reconnects are normal; only mention one that lingers.
      if (tunnel.status !== "connected") timer = window.setTimeout(() => setSlow(true), 3000);
    };
    tunnel.addEventListener("status", on);
    on();
    return () => { tunnel.removeEventListener("status", on); window.clearTimeout(timer); };
  }, [tunnel]);
  if (!tunnel || tunnel.status === "connected" || tunnel.status === "idle") return null;
  if (tunnel.status !== "revoked" && !slow) return null;
  return (
    <div className="relay-banner" role="status" data-status={tunnel.status}>
      {tunnel.status === "revoked"
        ? <>{t("app.relayBanner.revoked", { detail: tunnel.detail })}</>
        : <>{t("app.relayBanner.reaching", { detail: tunnel.detail })}</>}
    </div>
  );
}
