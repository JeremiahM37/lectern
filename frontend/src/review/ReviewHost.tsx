import { useEffect, useState } from "react";
import { createClient } from "../api/client";
import { SessionReview } from "./SessionReview";
import appCss from "../../../web/static/style.css?inline";
import sheetCss from "../board/board.css?inline";
import reviewCss from "./review.css?inline";

const api = { request: createClient() };

// The host page's element-wide rules (the terminal styles every header,
// dialog label and paragraph) are put back to neutral inside the workspace.
// Each selector here is exactly as specific as the rule it undoes and comes
// later, so the workspace's own class rules still win.
const resetCss = `
:where(#session-review) header { display: flex; padding: 0; border-bottom: 0; gap: 10px; }
dialog:where(#session-review) label { display: block; margin: 0; gap: 0; }
dialog:where(#session-review) label input, dialog:where(#session-review) label select { width: auto; }
dialog:where(#session-review) p { color: inherit; line-height: 1.5; }
:where(#session-review) footer { display: flex; justify-content: center; }
`;

/**
 * The Review & merge workspace, opened from a page that does not carry the
 * main app's stylesheets (the terminal page). The app's styles are added
 * only while the workspace is open and taken away when it closes, so the
 * terminal behind it keeps its own look.
 */
export function ReviewHost({
  sessionId,
  name,
  onClose,
  onNotice,
}: {
  sessionId: number;
  name?: string;
  onClose(): void;
  onNotice(text: string, error?: boolean): void;
}) {
  const [title, setTitle] = useState(name);
  useEffect(() => {
    if (name) return;
    api
      .request<{ name?: string }>(`/sessions/${sessionId}`)
      .then((row) => setTitle(row.name))
      .catch(() => {});
  }, [sessionId, name]);
  useEffect(() => {
    const style = document.createElement("style");
    style.dataset.lecternReview = "";
    style.textContent = [appCss, sheetCss, reviewCss, resetCss].join("\n");
    document.head.append(style);
    return () => style.remove();
  }, []);
  return <SessionReview api={api} sessionId={sessionId} name={title} onClose={onClose} onNotice={onNotice} />;
}
