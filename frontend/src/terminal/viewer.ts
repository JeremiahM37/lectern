import { ApiError } from "../api";
import { t } from "../i18n";

// viewerUnavailable recognises the server saying its web terminal server
// could not be started at all. That is not a hiccup to retry (a session still
// coming up is a plain 503 and is retried): say so once, with what helps.
export function viewerUnavailable(error: unknown): string | null {
  if (!(error instanceof ApiError) || error.status !== 503) return null;
  const payload = error.payload as { code?: unknown; reason?: unknown } | undefined;
  if (payload?.code !== "terminal_viewer_unavailable") return null;
  return t("terminal.viewerUnavailable", { reason: typeof payload.reason === "string" ? payload.reason : "" });
}
