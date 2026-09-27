// The bar a phone shows while text is selected in the live terminal (a long
// press, Engine.selectAt). The terminal keeps running underneath: output
// arrives, the selection stays on the text it was made on.
import { t, useLocale } from "../i18n";
import type { Engine } from "./engine";
import type { TerminalLink } from "./links";
import { copyClipboard, errorMessage } from "./model";

export function LiveSelectionBar({
  engine,
  onNotice,
  onLink,
}: {
  engine: () => Engine | undefined;
  onNotice: (text: string) => void;
  onLink: (link: TerminalLink) => void;
}) {
  useLocale();
  const e = engine();
  const link = e?.selectedLink();
  const keep = (event: React.PointerEvent) => event.preventDefault();
  return (
    <div id="select-bar" className="live-selection" role="toolbar" aria-label={t("select.label")}>
      <button
        id="select-copy"
        className="primary"
        onPointerDown={keep}
        onClick={() => {
          const text = e?.term.getSelection() || "";
          void copyClipboard(text, () => {})
            .then(() => {
              onNotice(text.includes("\n") ? t("select.copiedLines", { count: text.split("\n").length }) : t("select.copied"));
              e?.term.clearSelection();
            })
            .catch((error) => onNotice(errorMessage(error)));
        }}
      >
        {t("select.copy")}
      </button>
      <button id="select-lines" onPointerDown={keep} onClick={() => e?.selectLines()}>
        {t("select.lines")}
      </button>
      {link && (
        <button
          id="select-open"
          onPointerDown={keep}
          onClick={() => {
            e?.term.clearSelection();
            onLink(link);
          }}
        >
          {link.kind === "url" ? t("select.openLink") : t("select.openFile")}
        </button>
      )}
      <button id="select-clear" onPointerDown={keep} onClick={() => e?.term.clearSelection()}>
        {t("select.done")}
      </button>
    </div>
  );
}
