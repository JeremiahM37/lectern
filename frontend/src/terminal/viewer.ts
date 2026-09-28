import { ApiError } from "../api";
import { t } from "../i18n";
import type { NoticeAction } from "../types";

// missingViewer recognises the server saying its terminal viewer (ttyd) is not
// installed. That is not a hiccup to retry: the answer names the command that
// installs it, and the notice offers to copy it.
export function missingViewer(error: unknown): { message: string; action: NoticeAction } | null {
  if (!(error instanceof ApiError) || error.status !== 503) return null;
  const payload = error.payload as { code?: unknown; fix?: unknown } | undefined;
  if (payload?.code !== "terminal_viewer_missing" || typeof payload.fix !== "string") return null;
  const fix = payload.fix;
  return {
    message: t("terminal.viewerMissing", { command: fix }),
    action: {
      label: t("terminal.copyCommand"),
      run: () => navigator.clipboard?.writeText(fix),
    },
  };
}
