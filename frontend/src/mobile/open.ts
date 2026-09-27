// Opens a web address outside the app: a new tab in a browser, the phone's
// browser from the Android app (whose WebView never leaves its own
// Lectern). Only http and https.
import { nativeBridge } from "../native/bridge";

export function openExternal(url: string) {
  if (!/^https?:\/\//i.test(url)) return;
  const bridge = nativeBridge();
  if (bridge?.openUrl) {
    try {
      bridge.openUrl(url);
      return;
    } catch {
      /* fall back to the page */
    }
  }
  window.open(url, "_blank", "noopener");
}
