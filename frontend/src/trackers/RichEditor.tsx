import { useRef, useState } from "react";
import { Markdown } from "../sessions/markdown";
import { applyFormat, type Format } from "./format";

const TOOLS: { format: Format; label: string; title: string }[] = [
  { format: "bold", label: "B", title: "Bold (Ctrl+B)" },
  { format: "italic", label: "I", title: "Italic (Ctrl+I)" },
  { format: "strike", label: "S", title: "Strikethrough" },
  { format: "code", label: "</>", title: "Inline code" },
  { format: "link", label: "🔗", title: "Link (Ctrl+K)" },
  { format: "heading", label: "H", title: "Heading" },
  { format: "bullet", label: "•", title: "Bulleted list" },
  { format: "number", label: "1.", title: "Numbered list" },
  { format: "quote", label: "❝", title: "Quote" },
  { format: "codeblock", label: "{ }", title: "Code block" },
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
      <div className="th-editor-bar" role="toolbar" aria-label={`${label} formatting`}>
        <div className="th-editor-tabs">
          <button type="button" className={preview ? "" : "on"} aria-pressed={!preview} onClick={() => setPreview(false)}>
            Write
          </button>
          <button type="button" className={preview ? "on" : ""} aria-pressed={preview} onClick={() => setPreview(true)}>
            Preview
          </button>
        </div>
        {!preview && (
          <div className="th-editor-tools">
            {TOOLS.map((t) => (
              <button
                key={t.format}
                type="button"
                title={t.title}
                aria-label={t.title}
                // keep the textarea's selection when the button takes the tap
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => apply(t.format)}
              >
                {t.label}
              </button>
            ))}
          </div>
        )}
      </div>
      {preview ? (
        <div className="th-editor-preview th-md">{value.trim() ? <Markdown text={value} /> : <p className="th-muted">Nothing to preview.</p>}</div>
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
