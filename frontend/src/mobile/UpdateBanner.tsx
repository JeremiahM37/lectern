// "Lectern 2.9.0 is out · Update", in the Android app, when a newer release
// has an app (native/update.ts). Checked quietly when the app opens, at most
// every few hours; dismissing hides that version until the next one.
import { useEffect } from "react";
import { t, useLocale } from "../i18n";
import { checkForUpdateNow, dismissUpdate, installUpdate, updateDismissed, useUpdateStatus } from "../native/update";

export function UpdateBanner() {
  useLocale();
  const status = useUpdateStatus();
  useEffect(() => {
    checkForUpdateNow();
    const visible = () => !document.hidden && checkForUpdateNow();
    document.addEventListener("visibilitychange", visible);
    return () => document.removeEventListener("visibilitychange", visible);
  }, []);
  const working = status.state === "downloading" || status.state === "installing";
  const failed = status.state === "error" && !!status.latest;
  if (!working && !failed && (status.state !== "available" || updateDismissed(status.latest))) return null;
  return (
    <div id="update-banner" role="status" data-state={status.state}>
      <span>
        {status.state === "downloading"
          ? t("appUpdate.downloading", { progress: status.progress ?? 0 })
          : status.state === "installing"
            ? t("appUpdate.installing")
            : failed
              ? t("appUpdate.failed", { error: status.error ?? "" })
              : t("appUpdate.banner", { version: status.latest ?? "" })}
      </span>
      {!working && (
        <>
          <button className="b ok" onClick={installUpdate}>{failed ? t("appUpdate.retry") : t("appUpdate.update")}</button>
          <button className="b" aria-label={t("appUpdate.later")} onClick={() => dismissUpdate(status.latest ?? "")}>{t("appUpdate.later")}</button>
        </>
      )}
    </div>
  );
}
