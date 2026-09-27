// Rich Markdown editing: type into the rendered document. Built on TipTap
// (ProseMirror), loaded only when a document is switched to Rich. The
// Markdown extension parses the file and writes it back; front matter is kept
// aside and restored unchanged. Nothing is written until the text is edited,
// and then the Markdown comes back in a standard form (for example, `*`
// emphasis may become `_`), which the note above the editor says.
import { Editor } from "@tiptap/core";
import { TableKit } from "@tiptap/extension-table";
import { TaskItem, TaskList } from "@tiptap/extension-list";
import { Markdown } from "@tiptap/markdown";
import StarterKit from "@tiptap/starter-kit";
import { useEffect, useRef, useState } from "react";
import { t } from "../i18n";
import { splitFrontMatter } from "./markdown-model";

export default function RichMarkdown({
  value,
  revision,
  readOnly,
  onChange,
  onSave,
}: {
  value: string;
  revision: number;
  readOnly: boolean;
  onChange: (value: string) => void;
  onSave: () => void;
}) {
  const host = useRef<HTMLDivElement>(null);
  const editor = useRef<Editor>(undefined);
  const prefix = useRef("");
  const latest = useRef({ onChange, onSave });
  latest.current = { onChange, onSave };
  const [, setTick] = useState(0);
  const load = (text: string) => {
    const front = splitFrontMatter(text);
    prefix.current = text.slice(0, text.length - front.body.length);
    return front.body;
  };
  useEffect(() => {
    if (!host.current) return;
    const instance = new Editor({
      element: host.current,
      extensions: [StarterKit, TableKit, TaskList, TaskItem.configure({ nested: true }), Markdown],
      content: load(value),
      contentType: "markdown",
      editable: !readOnly,
      editorProps: {
        attributes: { class: "wb-md-body wb-rich", "aria-label": t("files.rich"), role: "textbox", "aria-multiline": "true" },
        handleKeyDown: (_view, event) => {
          if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") {
            event.preventDefault();
            latest.current.onSave();
            return true;
          }
          return false;
        },
      },
      onUpdate: ({ editor: current }) => latest.current.onChange(prefix.current + current.getMarkdown()),
      onSelectionUpdate: () => setTick((n) => n + 1),
      onTransaction: () => setTick((n) => n + 1),
    });
    editor.current = instance;
    return () => {
      instance.destroy();
      editor.current = undefined;
    };
  }, []);
  // A reload from disk replaces the document.
  useEffect(() => {
    if (revision && editor.current) editor.current.commands.setContent(load(value), { contentType: "markdown", emitUpdate: false });
  }, [revision]);
  useEffect(() => {
    editor.current?.setEditable(!readOnly);
  }, [readOnly]);
  const e = editor.current;
  const tools: [string, string, () => void, boolean][] = e
    ? [
        ["B", t("files.bold"), () => e.chain().focus().toggleBold().run(), e.isActive("bold")],
        ["I", t("files.italic"), () => e.chain().focus().toggleItalic().run(), e.isActive("italic")],
        ["H1", t("files.heading") + " 1", () => e.chain().focus().toggleHeading({ level: 1 }).run(), e.isActive("heading", { level: 1 })],
        ["H2", t("files.heading") + " 2", () => e.chain().focus().toggleHeading({ level: 2 }).run(), e.isActive("heading", { level: 2 })],
        ["`", t("files.inlineCode"), () => e.chain().focus().toggleCode().run(), e.isActive("code")],
        ["•", t("files.bulleted"), () => e.chain().focus().toggleBulletList().run(), e.isActive("bulletList")],
        ["1.", t("files.numbered"), () => e.chain().focus().toggleOrderedList().run(), e.isActive("orderedList")],
        ["☐", t("files.task"), () => e.chain().focus().toggleTaskList().run(), e.isActive("taskList")],
        ["❝", t("files.quote"), () => e.chain().focus().toggleBlockquote().run(), e.isActive("blockquote")],
        ["{ }", t("files.codeBlock"), () => e.chain().focus().toggleCodeBlock().run(), e.isActive("codeBlock")],
        ["▦", t("files.table"), () => e.chain().focus().insertTable({ rows: 3, cols: 2, withHeaderRow: true }).run(), false],
        ["↶", t("files.undo"), () => e.chain().focus().undo().run(), false],
        ["↷", t("files.redo"), () => e.chain().focus().redo().run(), false],
      ]
    : [];
  return (
    <div className="wb-rich-wrap">
      {!readOnly && (
        <div className="wb-md-tools" role="toolbar" aria-label={t("files.formatting")}>
          {tools.map(([label, name, run, on]) => (
            <button key={name} aria-label={name} title={name} aria-pressed={on} onMouseDown={(event) => event.preventDefault()} onClick={run}>
              {label}
            </button>
          ))}
        </div>
      )}
      <div ref={host} className="wb-rich-host" />
    </div>
  );
}
