import { t } from "../i18n";
import { forwardRef, useEffect, useImperativeHandle, useLayoutEffect, useRef, useState } from "react";
import type { LineTarget } from "./deeplink";
import { languageFor } from "./formats";

type MonacoModule = typeof import("./monaco");
type StandaloneEditor = ReturnType<MonacoModule["createEditor"]>["editor"];

let loading: Promise<MonacoModule> | undefined;
/** Load the full editor once; later opens reuse it. */
export function loadMonaco(): Promise<MonacoModule> {
  loading ??= import("./monaco").catch((error) => {
    loading = undefined;
    throw error;
  });
  return loading;
}

/** Formatting actions the Markdown toolbar applies to whichever editor is showing. */
export interface EditorHandle {
  wrap(before: string, after: string, placeholder: string): void;
  linePrefix(prefix: string): void;
  insert(text: string): void;
  reveal(line: number): void;
  focus(): void;
  topLine(): number;
}

export interface EditorProps {
  path: string;
  value: string;
  /** Bumped when the text is replaced from outside (a reload from disk). */
  revision: number;
  readOnly: boolean;
  full: boolean;
  fontSize: number;
  minimap: boolean;
  wordWrap: boolean;
  target?: LineTarget;
  targetKey?: number;
  onChange: (value: string) => void;
  onSave: () => void;
  onLine: (target: LineTarget) => void;
  onScrollLine?: (line: number) => void;
  onFallback?: (message: string) => void;
}

export const CodeEditor = forwardRef<EditorHandle, EditorProps>(function CodeEditor(props, ref) {
  return props.full ? <MonacoEditor {...props} ref={ref} /> : <PlainEditor {...props} ref={ref} />;
});

const MonacoEditor = forwardRef<EditorHandle, EditorProps>(function MonacoEditor(props, ref) {
  const host = useRef<HTMLDivElement>(null);
  const editor = useRef<StandaloneEditor>(undefined);
  const monacoRef = useRef<MonacoModule["monaco"]>(undefined);
  const latest = useRef(props);
  latest.current = props;
  const decorations = useRef<{ clear(): void } | undefined>(undefined);
  const [ready, setReady] = useState(false);
  useEffect(() => {
    let disposed = false;
    const cleanups: (() => void)[] = [];
    void loadMonaco()
      .then((module) => {
        if (disposed || !host.current) return;
        const { editor: instance, model } = module.createEditor(host.current, {
          value: latest.current.value,
          language: languageFor(latest.current.path),
          path: latest.current.path,
          readOnly: latest.current.readOnly,
          fontSize: latest.current.fontSize,
          minimap: latest.current.minimap,
          wordWrap: latest.current.wordWrap,
        });
        const { monaco } = module;
        monacoRef.current = monaco;
        editor.current = instance;
        instance.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => latest.current.onSave());
        const change = model.onDidChangeContent(() => latest.current.onChange(model.getValue()));
        const mouse = instance.onMouseDown((event) => {
          if (event.target.type === monaco.editor.MouseTargetType.GUTTER_LINE_NUMBERS && event.target.position) {
            const line = event.target.position.lineNumber;
            const anchor = instance.getSelection();
            // Shift-click in the gutter links a range, as code hosts do.
            if (event.event.shiftKey && anchor && anchor.startLineNumber !== line)
              latest.current.onLine({ line: Math.min(anchor.startLineNumber, line), endLine: Math.max(anchor.startLineNumber, line) });
            else latest.current.onLine({ line });
          }
        });
        const scroll = instance.onDidScrollChange(() => {
          const range = instance.getVisibleRanges()[0];
          if (range) latest.current.onScrollLine?.(range.startLineNumber);
        });
        cleanups.push(() => {
          change.dispose();
          mouse.dispose();
          scroll.dispose();
          instance.dispose();
        });
        setReady(true);
      })
      .catch((error: unknown) => {
        if (!disposed) latest.current.onFallback?.(error instanceof Error ? error.message : String(error));
      });
    return () => {
      disposed = true;
      editor.current = undefined;
      cleanups.forEach((cleanup) => cleanup());
    };
  }, [props.path]);
  // A reload from disk replaces the text as one undoable edit instead of
  // wiping the history. Only a new revision counts: the value prop also
  // carries our own typing back through React, often a few keystrokes behind
  // the model, and applying that would undo what was just typed.
  useEffect(() => {
    const instance = editor.current,
      model = instance?.getModel();
    if (!instance || !model || model.getValue() === latest.current.value) return;
    const position = instance.getPosition();
    model.pushEditOperations([], [{ range: model.getFullModelRange(), text: latest.current.value }], () => null);
    if (position) instance.setPosition(position);
  }, [props.revision]);
  useEffect(() => {
    editor.current?.updateOptions({
      readOnly: props.readOnly,
      fontSize: props.fontSize,
      minimap: { enabled: props.minimap },
      wordWrap: props.wordWrap ? "on" : "off",
    });
  }, [props.readOnly, props.fontSize, props.minimap, props.wordWrap, ready]);
  useEffect(() => {
    const instance = editor.current,
      monaco = monacoRef.current,
      target = props.target;
    if (!instance || !monaco || !target) return;
    const end = target.endLine || target.line;
    instance.revealLinesInCenter(target.line, end);
    instance.setPosition({ lineNumber: target.line, column: target.column || 1 });
    decorations.current?.clear();
    decorations.current = instance.createDecorationsCollection([
      { range: new monaco.Range(target.line, 1, end, 1), options: { isWholeLine: true, className: "wb-target-line", linesDecorationsClassName: "wb-target-gutter" } },
    ]);
  }, [props.targetKey, ready]);
  useImperativeHandle(ref, () => ({
    wrap(before, after, placeholder) {
      const instance = editor.current,
        model = instance?.getModel(),
        selection = instance?.getSelection();
      if (!instance || !model || !selection) return;
      const text = model.getValueInRange(selection) || placeholder;
      instance.executeEdits("format", [{ range: selection, text: before + text + after, forceMoveMarkers: true }]);
      instance.focus();
    },
    linePrefix(prefix) {
      const instance = editor.current,
        model = instance?.getModel(),
        selection = instance?.getSelection(),
        monaco = monacoRef.current;
      if (!instance || !model || !selection || !monaco) return;
      const edits = [];
      for (let line = selection.startLineNumber; line <= selection.endLineNumber; line++)
        edits.push({ range: new monaco.Range(line, 1, line, 1), text: prefix });
      instance.executeEdits("format", edits);
      instance.focus();
    },
    insert(text) {
      const instance = editor.current,
        selection = instance?.getSelection();
      if (!instance || !selection) return;
      instance.executeEdits("format", [{ range: selection, text, forceMoveMarkers: true }]);
      instance.focus();
    },
    reveal(line) {
      editor.current?.revealLineNearTop(line);
      editor.current?.setPosition({ lineNumber: line, column: 1 });
      editor.current?.focus();
    },
    focus() {
      editor.current?.focus();
    },
    topLine() {
      return editor.current?.getVisibleRanges()[0]?.startLineNumber || 1;
    },
  }));
  return (
    <div className="wb-monaco" data-ready={ready || undefined}>
      <div ref={host} className="wb-monaco-host" />
      {!ready && (
        // The text is readable at once; the full editor replaces this view.
        <pre className="wb-monaco-placeholder">{props.value.slice(0, 200000)}</pre>
      )}
    </div>
  );
});

// Phones read far more than they edit. This view costs nothing to load, keeps
// line numbers for links, and edits in a plain text area.
const PLAIN_LIMIT = 20000;
const PlainEditor = forwardRef<EditorHandle, EditorProps>(function PlainEditor(props, ref) {
  const area = useRef<HTMLTextAreaElement>(null),
    lines = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (!props.target) return;
    if (props.readOnly) {
      lines.current?.querySelector(`[data-line="${props.target.line}"]`)?.scrollIntoView({ block: "center" });
    } else if (area.current) {
      const node = area.current,
        offset = props.value.split("\n").slice(0, props.target.line - 1).join("\n").length + (props.target.line > 1 ? 1 : 0);
      node.setSelectionRange(offset, offset);
      node.scrollTop = Math.max(0, (props.target.line - 5) * (node.scrollHeight / Math.max(1, props.value.split("\n").length)));
    }
  }, [props.targetKey, props.readOnly]);
  function replace(start: number, end: number, text: string, select?: [number, number]) {
    const node = area.current;
    if (!node) return;
    node.focus();
    node.setSelectionRange(start, end);
    // execCommand keeps the browser's own undo stack; setRangeText is the fallback.
    if (!document.execCommand("insertText", false, text)) node.setRangeText(text, start, end, "end");
    if (select) node.setSelectionRange(select[0], select[1]);
    props.onChange(node.value);
  }
  useImperativeHandle(ref, () => ({
    wrap(before, after, placeholder) {
      const node = area.current;
      if (!node) return;
      const { selectionStart: start, selectionEnd: end } = node;
      const text = node.value.slice(start, end) || placeholder;
      replace(start, end, before + text + after, [start + before.length, start + before.length + text.length]);
    },
    linePrefix(prefix) {
      const node = area.current;
      if (!node) return;
      const start = node.value.lastIndexOf("\n", node.selectionStart - 1) + 1;
      const end = node.selectionEnd;
      const block = node.value.slice(start, end);
      replace(start, end, block.split("\n").map((line) => prefix + line).join("\n"));
    },
    insert(text) {
      const node = area.current;
      if (node) replace(node.selectionStart, node.selectionEnd, text);
    },
    reveal(line) {
      lines.current?.querySelector(`[data-line="${line}"]`)?.scrollIntoView({ block: "start" });
    },
    focus() {
      area.current?.focus();
    },
    topLine() {
      return 1;
    },
  }));
  if (!props.readOnly)
    return (
      <textarea
        ref={area}
        className="wb-plain-edit"
        aria-label={t("files.editLabel", { path: props.path })}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        value={props.value}
        style={{ fontSize: props.fontSize, whiteSpace: props.wordWrap ? "pre-wrap" : "pre" }}
        onChange={(event) => props.onChange(event.target.value)}
        onKeyDown={(event) => {
          if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") {
            event.preventDefault();
            props.onSave();
          }
          if (event.key === "Tab" && !event.shiftKey && !event.altKey && !event.ctrlKey) {
            event.preventDefault();
            const node = event.currentTarget;
            replace(node.selectionStart, node.selectionEnd, "  ");
          }
        }}
      />
    );
  const all = props.value.split("\n");
  const shown = all.slice(0, PLAIN_LIMIT);
  const start = props.target?.line || 0,
    end = props.target?.endLine || start;
  return (
    <div ref={lines} className={"wb-plain" + (props.wordWrap ? " wrap" : "")} style={{ fontSize: props.fontSize }} role="document" aria-label={props.path}>
      {shown.map((text, index) => {
        const number = index + 1;
        return (
          <div key={index} className={"wb-plain-line" + (number >= start && number <= end ? " target" : "")} data-line={number}>
            <a
              className="wb-ln"
              href={"#L" + number}
              aria-label={t("files.lineN", { n: number })}
              onClick={(event) => {
                event.preventDefault();
                props.onLine({ line: number });
              }}
            >
              {number}
            </a>
            <span>{text || "​"}</span>
          </div>
        );
      })}
      {all.length > PLAIN_LIMIT && <p className="wb-note">{t("files.plainLimit", { count: PLAIN_LIMIT.toLocaleString() })}</p>}
    </div>
  );
});
