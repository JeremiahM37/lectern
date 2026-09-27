// Loaded only when a file is opened in the full editor, so the terminal page
// and the phone's first load never download it. The whole editor and its
// workers ship in the binary; nothing is fetched from a CDN.
import * as monaco from "monaco-editor";
import EditorWorker from "monaco-editor/editor/editor.worker.js?worker";
import CssWorker from "monaco-editor/languages/features/css/css.worker.js?worker";
import HtmlWorker from "monaco-editor/languages/features/html/html.worker.js?worker";
import JsonWorker from "monaco-editor/languages/features/json/json.worker.js?worker";
import TsWorker from "monaco-editor/languages/features/typescript/ts.worker.js?worker";

export { monaco };

// Monaco finds its core worker through a runtime URL the bundler cannot
// follow. Once MonacoEnvironment exists it is asked for every worker, so it
// hands out the bundled worker for each label.
(globalThis as { MonacoEnvironment?: unknown }).MonacoEnvironment = {
  getWorker(_id: string, label: string) {
    if (label === "json") return new JsonWorker();
    if (label === "css" || label === "scss" || label === "less") return new CssWorker();
    if (label === "html" || label === "handlebars" || label === "razor") return new HtmlWorker();
    if (label === "typescript" || label === "javascript") return new TsWorker();
    return new EditorWorker();
  },
};

// The TypeScript service runs on one file with no project around it, so its
// checks would flag every import as unresolved. Keep what works on a single
// file (colouring, completion, hover, go to definition in the file, rename)
// and turn the diagnostics off; JSX parses in .tsx and .jsx files. Settings
// adapted from Orca's monaco-setup.ts (github.com/stablyai/orca, MIT).
const quiet = { noSemanticValidation: true, noSuggestionDiagnostics: true, noSyntaxValidation: true };
for (const defaults of [monaco.typescript.typescriptDefaults, monaco.typescript.javascriptDefaults]) {
  defaults.setDiagnosticsOptions(quiet);
  defaults.setCompilerOptions({ ...defaults.getCompilerOptions(), jsx: monaco.typescript.JsxEmit.Preserve, allowJs: true });
}

// Monaco follows the app's light or dark theme, including a switch made while
// an editor is open.
const themeName = () => (document.documentElement.dataset.theme === "light" ? "vs" : "vs-dark");
new MutationObserver(() => monaco.editor.setTheme(themeName())).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });

let registered = false;

/** Markdown gets a "/" block menu, like a document editor's slash commands. */
function registerMarkdownBlocks() {
  if (registered) return;
  registered = true;
  const blocks: [string, string, string][] = [
    ["Heading 1", "# ", "Large section heading"],
    ["Heading 2", "## ", "Section heading"],
    ["Heading 3", "### ", "Subsection heading"],
    ["Bulleted list", "- ", "A plain list"],
    ["Numbered list", "1. ", "An ordered list"],
    ["Task list", "- [ ] ", "A checklist item"],
    ["Quote", "> ", "A block quote"],
    ["Code block", "```${1:language}\n$0\n```", "Fenced code"],
    ["Table", "| ${1:Column} | ${2:Column} |\n| --- | --- |\n| $0 |  |", "A two-column table"],
    ["Mermaid diagram", "```mermaid\nflowchart LR\n  ${1:A} --> ${2:B}\n```", "A rendered diagram"],
    ["Divider", "---\n", "A horizontal rule"],
    ["Front matter", "---\ntitle: ${1:Title}\n---\n", "Document metadata"],
  ];
  monaco.languages.registerCompletionItemProvider("markdown", {
    triggerCharacters: ["/"],
    provideCompletionItems(model, position) {
      const before = model.getLineContent(position.lineNumber).slice(0, position.column - 1);
      const slash = /(^|\s)\/(\w*)$/.exec(before);
      if (!slash) return { suggestions: [] };
      const start = position.column - slash[2]!.length - 1;
      const range = new monaco.Range(position.lineNumber, start, position.lineNumber, position.column);
      return {
        suggestions: blocks.map(([label, text, detail], index) => ({
          label: "/" + label,
          filterText: "/" + label,
          sortText: String(index).padStart(2, "0"),
          kind: monaco.languages.CompletionItemKind.Snippet,
          insertText: text,
          insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet,
          detail,
          range,
        })),
      };
    },
  });
}

export interface EditorOptions {
  value: string;
  language: string;
  path: string;
  readOnly: boolean;
  fontSize: number;
  minimap: boolean;
  wordWrap: boolean;
}

export function createEditor(host: HTMLElement, options: EditorOptions) {
  registerMarkdownBlocks();
  // One model per path, so undo history survives switching between tabs.
  const uri = modelUri(options.path);
  const model = monaco.editor.getModel(uri) || monaco.editor.createModel(options.value, options.language, uri);
  if (model.getValue() !== options.value) model.setValue(options.value);
  monaco.editor.setModelLanguage(model, options.language);
  const editor = monaco.editor.create(host, {
    model,
    theme: themeName(),
    readOnly: options.readOnly,
    fontSize: options.fontSize,
    minimap: { enabled: options.minimap },
    wordWrap: options.wordWrap ? "on" : "off",
    automaticLayout: true,
    scrollBeyondLastLine: false,
    renderWhitespace: "selection",
    multiCursorModifier: "alt",
    fixedOverflowWidgets: true,
    "semanticHighlighting.enabled": true,
  });
  return { editor, model };
}

export function createDiff(host: HTMLElement, original: string, modified: string, language: string) {
  const diff = monaco.editor.createDiffEditor(host, {
    theme: themeName(),
    automaticLayout: true,
    readOnly: true,
    originalEditable: false,
    renderSideBySide: host.clientWidth > 700,
  });
  const left = monaco.editor.createModel(original, language),
    right = monaco.editor.createModel(modified, language);
  diff.setModel({ original: left, modified: right });
  return {
    dispose() {
      diff.dispose();
      left.dispose();
      right.dispose();
    },
  };
}

/** Forget a closed file's model (and its undo history). */
export function releaseModel(path: string) {
  monaco.editor.getModel(modelUri(path))?.dispose();
}

// A workspace path is relative; a file outside the workspace is absolute or
// ~/, under its own scheme so the two can never name the same model (and a
// URI path may not start with "//").
function modelUri(path: string) {
  if (path.startsWith("~/")) return monaco.Uri.from({ scheme: "lectern-outside", path: "/" + path });
  if (path.startsWith("/")) return monaco.Uri.from({ scheme: "lectern-outside", path });
  return monaco.Uri.from({ scheme: "lectern-file", path: "/" + path });
}
