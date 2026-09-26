// Renders one merged tool_use/tool_result as a card instead of the raw JSON
// dump the reader used to show. Collapsed by default (a <details>/<summary>,
// same pattern the codebase already uses for changed-files and detail rows —
// no extra state, and <summary> is a real block-level tap target on a
// phone); one tap expands. See docs/mobile-sessions.md for the design.
import { DiffViewer } from "../../review/DiffViewer";
import type { FilePatch } from "../../review/types";
import { describeTool, type ToolCategory } from "./describe";
import { buildMultiEditPatch, buildNewFilePatch, buildReplacePatch, parseCodexPatch } from "./patch";
import type { ToolCard } from "./chatCards";

const ICON: Record<ToolCategory, string> = {
  read: "📄",
  edit: "✏️",
  terminal: "⌘",
  search: "🔎",
  web: "🌐",
  task: "🧩",
  todo: "☑️",
  question: "❓",
  mcp: "🧰",
  other: "⚙️",
};

function str(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

function editDiffFiles(name: string, input: Record<string, unknown>): FilePatch[] {
  const path = str(input.file_path) ?? "file";
  if (name === "Write") {
    return [{ path, patch: buildNewFilePatch(path, str(input.content) ?? "") }];
  }
  if (name === "MultiEdit" && Array.isArray(input.edits)) {
    const edits = input.edits.filter(
      (e): e is { old_string: string; new_string: string } =>
        !!e && typeof e.old_string === "string" && typeof e.new_string === "string",
    );
    return [{ path, patch: buildMultiEditPatch(path, edits) }];
  }
  if (name === "apply_patch" || name === "CodexPatch") {
    const raw = str(input.patch) ?? str(input.input) ?? "";
    return parseCodexPatch(raw).map((f) => ({ path: f.path, patch: f.patch }));
  }
  // Edit / GeminiEdit
  return [{ path, patch: buildReplacePatch(path, str(input.old_string) ?? "", str(input.new_string) ?? "") }];
}

function Todos({ input }: { input: Record<string, unknown> }) {
  const todos = Array.isArray(input.todos) ? input.todos : [];
  if (!todos.length) return <p className="sub">No items.</p>;
  return (
    <ul className="tool-todos">
      {todos.map((todo, i) => {
        const t = todo as Record<string, unknown>;
        const status = str(t.status) ?? "pending";
        const mark = status === "completed" ? "☑" : status === "in_progress" ? "◐" : "☐";
        const text = str(t.content) ?? str(t.activeForm) ?? JSON.stringify(t);
        return (
          <li key={i} data-status={status}>
            <span className="tool-todo-mark">{mark}</span> {text}
          </li>
        );
      })}
    </ul>
  );
}

function PrettyArgs({ input }: { input: Record<string, unknown> }) {
  if (!Object.keys(input).length) return null;
  return (
    <details className="tool-args">
      <summary>Arguments</summary>
      <pre>{JSON.stringify(input, null, 2)}</pre>
    </details>
  );
}

function Output({ card }: { card: ToolCard }) {
  if (card.status === "running") return <p className="sub tool-running">Running…</p>;
  if (card.output == null) return null;
  return (
    <div className="tool-output" data-error={card.isError ? "true" : undefined}>
      <div className="tool-output-label">{card.isError ? "Failed" : "Output"}</div>
      <pre>{card.output || "(no output)"}</pre>
      {card.outputTruncated && <p className="sub">Output was shortened.</p>}
    </div>
  );
}

export function ToolCardView({ card }: { card: ToolCard }) {
  const summary = describeTool(card.name, card.input);
  const isEdit = summary.category === "edit" && card.name !== "TodoWrite";
  return (
    <details className="tool-card" data-category={summary.category}>
      <summary>
        <span className="tool-icon" aria-hidden="true">
          {ICON[summary.category]}
        </span>
        <span className="tool-title">{summary.title}</span>
        {summary.subtitle && <span className="tool-sep">·</span>}
        {summary.subtitle && <span className="tool-subtitle">{summary.subtitle}</span>}
        {card.status === "running" && <span className="tool-spinner" aria-label="running">…</span>}
        {card.status === "done" && card.isError && (
          <span className="tool-fail" aria-label="failed">
            ✕
          </span>
        )}
      </summary>
      <div className="tool-body">
        {summary.category === "todo" ? (
          <Todos input={card.input} />
        ) : isEdit ? (
          <DiffViewer files={editDiffFiles(card.name, card.input)} stats={[]} wrap />
        ) : summary.category === "terminal" ? (
          <>
            <pre className="tool-command">
              {typeof card.input.command === "string"
                ? card.input.command
                : Array.isArray(card.input.command)
                  ? card.input.command.join(" ")
                  : summary.title}
            </pre>
            <Output card={card} />
          </>
        ) : (
          <>
            <PrettyArgs input={card.input} />
            <Output card={card} />
          </>
        )}
      </div>
    </details>
  );
}
