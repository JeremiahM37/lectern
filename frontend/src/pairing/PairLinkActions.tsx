// Under a pairing QR code: send the same link by message or email instead
// of scanning it, or open it in the Android app on this very phone.
import { t } from "../i18n";
import { copyClipboard } from "../terminal/model";
import { appPairLink } from "./links";

export function PairLinkActions({ link, onNotice }: { link: string; onNotice(text: string, error?: boolean): void }) {
  const app = appPairLink(link);
  const canShare = typeof navigator !== "undefined" && typeof navigator.share === "function";
  return (
    <div className="pair-link-actions">
      <button
        className="b"
        data-testid="copy-pair-link"
        onClick={() =>
          void copyClipboard(link, () => {})
            .then(() => onNotice(t("pairLink.copied")))
            .catch((error) => onNotice(String(error), true))
        }
      >
        {t("pairLink.copy")}
      </button>
      {canShare && (
        <button
          className="b"
          onClick={() => void navigator.share({ title: t("pairLink.shareTitle"), url: link }).catch(() => undefined)}
        >
          {t("pairLink.share")}
        </button>
      )}
      {app && (
        <a className="b" href={app} data-testid="app-pair-link" title={t("pairLink.appTitle")}>
          {t("pairLink.openApp")}
        </a>
      )}
    </div>
  );
}

/** On a pairing page opened in a phone's browser: the same pairing in the
 * app instead, when it is installed. */
export function OpenInAppNote({ intent }: { intent?: string }) {
  if (!intent) return null;
  return (
    <p className="pair-open-app" data-testid="open-in-app">
      {t("pairLink.usingApp")} <a href={intent}>{t("pairLink.pairApp")}</a>
    </p>
  );
}
