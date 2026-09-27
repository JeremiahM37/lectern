// The bar a phone shows while text is selected in the live terminal (a long
// press, Engine.selectAt). The terminal keeps running underneath: output
// arrives, the selection stays on the text it was made on.
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
  const e = engine();
  const link = e?.selectedLink();
  const keep = (event: React.PointerEvent) => event.preventDefault();
  return (
    <div id="select-bar" className="live-selection" role="toolbar" aria-label="Text selection">
      <button
        id="select-copy"
        className="primary"
        onPointerDown={keep}
        onClick={() => {
          const text = e?.term.getSelection() || "";
          void copyClipboard(text, () => {})
            .then(() => {
              onNotice(text.includes("\n") ? `Copied ${text.split("\n").length} lines.` : "Copied.");
              e?.term.clearSelection();
            })
            .catch((error) => onNotice(errorMessage(error)));
        }}
      >
        Copy
      </button>
      <button id="select-lines" onPointerDown={keep} onClick={() => e?.selectLines()}>
        Whole lines
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
          {link.kind === "url" ? "Open link" : "Open file"}
        </button>
      )}
      <button id="select-clear" onPointerDown={keep} onClick={() => e?.term.clearSelection()}>
        Done
      </button>
    </div>
  );
}
