import { useRef, useState } from "react";
import { t, useLocale } from "../i18n";
import { Markdown } from "../sessions/markdown";
import { applyFormat, type Format } from "./format";

// Labels are the toolbar's glyphs and stay as they are.
const TOOLS = (): { format: Format; label: string; title: string }[] => [
  { format: "bold", label: "B", title: t("trackers.editor.bold") },
  { format: "italic", label: "I", title: t("trackers.editor.italic") },
  { format: "strike", label: "S", title: t("trackers.editor.strike") },
  { format: "code", label: "</>", title: t("trackers.editor.code") },
  { format: "link", label: "🔗", title: t("trackers.editor.link") },
  { format: "heading", label: "H", title: t("trackers.editor.heading") },
  { format: "bullet", label: "•", title: t("trackers.editor.bullet") },
  { format: "number", label: "1.", title: t("trackers.editor.number") },
  { format: "quote", label: "❝", title: t("trackers.editor.quote") },
  { format: "codeblock", label: "{ }", title: t("trackers.editor.codeblock") },
];

/** A compact Markdown editor: a toolbar, keyboard shortcuts and a Write /
 * Preview switch. What it produces is Markdown; the server turns it into
 * what the tracker stores (ADF for Jira Cloud, wiki markup for Jira Server,
 * Markdown as-is elsewhere). Works with touch: the toolbar buttons keep the
 * textarea's selection. */
export function RichEditor({
  value,
  onChange,
  placeholder,
  rows = 5,
  label,
  autoFocus,
}: {
  value: string;
  onChange(v: string): void;
  placeholder?: string;
  rows?: number;
  label: string;
  autoFocus?: boolean;
}) {
  useLocale();
  const [preview, setPreview] = useState(false);
  const ref = useRef<HTMLTextAreaElement>(null);
  function apply(format: Format) {
    const ta = ref.current;
    if (!ta) return;
    const res = applyFormat(value, ta.selectionStart, ta.selectionEnd, format);
    onChange(res.text);
    requestAnimationFrame(() => {
      ta.focus();
      ta.setSelectionRange(res.start, res.end);
    });
  }
  return (
    <div className="th-editor">
      <div className="th-editor-bar" role="toolbar" aria-label={t("trackers.editor.toolbar", { label })}>
        <div className="th-editor-tabs">
          <button type="button" className={preview ? "" : "on"} aria-pressed={!preview} onClick={() => setPreview(false)}>
            {t("trackers.editor.write")}
          </button>
          <button type="button" className={preview ? "on" : ""} aria-pressed={preview} onClick={() => setPreview(true)}>
            {t("trackers.editor.preview")}
          </button>
        </div>
        {!preview && (
          <div className="th-editor-tools">
            {TOOLS().map((tool) => (
              <button
                key={tool.format}
                type="button"
                title={tool.title}
                aria-label={tool.title}
                // keep the textarea's selection when the button takes the tap
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => apply(tool.format)}
              >
                {tool.label}
              </button>
            ))}
          </div>
        )}
      </div>
      {preview ? (
        <div className="th-editor-preview th-md">{value.trim() ? <Markdown text={value} /> : <p className="th-muted">{t("trackers.editor.nothingToPreview")}</p>}</div>
      ) : (
        <textarea
          ref={ref}
          aria-label={label}
          rows={rows}
          value={value}
          placeholder={placeholder}
          autoFocus={autoFocus}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            if (!(e.ctrlKey || e.metaKey)) return;
            const f = ({ b: "bold", i: "italic", k: "link" } as Record<string, Format>)[e.key.toLowerCase()];
            if (f) {
              e.preventDefault();
              apply(f);
            }
          }}
        />
      )}
    </div>
  );
}
