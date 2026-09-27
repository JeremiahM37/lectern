// Under a pairing QR code: send the same link by message or email instead
// of scanning it, or open it in the Android app on this very phone.
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
            .then(() => onNotice("Pairing link copied. It works once, for a few minutes."))
            .catch((error) => onNotice(String(error), true))
        }
      >
        Copy link
      </button>
      {canShare && (
        <button
          className="b"
          onClick={() => void navigator.share({ title: "Pair with Lectern", url: link }).catch(() => undefined)}
        >
          Share…
        </button>
      )}
      {app && (
        <a className="b" href={app} data-testid="app-pair-link" title="For the Lectern Android app on this device">
          Open in Android app
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
      Using the Lectern Android app? <a href={intent}>Pair it instead</a>.
    </p>
  );
}
