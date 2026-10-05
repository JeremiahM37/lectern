// Turns a tool_use name+input into the one-line summary a collapsed chat
// card shows — "Read /etc/hosts", "git status", "Search(pattern: TODO)" —
// instead of a JSON dump. Pure and DOM-free so it is unit-testable directly
// (see describe.test.ts), the same way frontend/src/review/diffLines.ts is.
// Modeled on Happy's knownTools registry (packages/happy-app/sources/
// components/tools/knownTools.tsx): title/subtitle/category per known tool
// name, one shared fallback for anything not listed.
import { t } from "../../i18n";

export type ToolCategory =
  | "read"
  | "edit"
  | "terminal"
  | "search"
  | "web"
  | "task"
  | "todo"
  | "question"
  | "mcp"
  | "other";

export interface ToolSummary {
  /** The line a collapsed card shows first — usually the target, not the tool name. */
  title: string;
  /** A shorter second line, shown under the title when there is room. */
  subtitle?: string;
  category: ToolCategory;
}

function str(v: unknown): string | undefined {
  return typeof v === "string" && v.length ? v : undefined;
}
function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + "…" : s;
}
function basename(p: string): string {
  const parts = p.split("/");
  return parts[parts.length - 1] || p;
}
function commandOf(input: Record<string, unknown>): string {
  const raw = input.command;
  if (typeof raw === "string") return raw;
  if (Array.isArray(raw)) return raw.filter((part) => typeof part === "string").join(" ");
  return "";
}

/** Formats an MCP tool's wire name (mcp__server__tool) as "server · tool". */
export function formatMCPTitle(name: string): string {
  const parts = name.split("__").filter(Boolean);
  if (parts.length < 2) return name;
  const server = parts[1] ?? name;
  const rest = parts.slice(2);
  const tool = rest.join("__") || server;
  return rest.length ? `${server} · ${tool}` : server;
}

export function describeTool(
  name: string,
  input: Record<string, unknown>,
): ToolSummary {
  if (name.startsWith("mcp__")) {
    return { title: formatMCPTitle(name), category: "mcp" };
  }
  switch (name) {
    case "Read":
    case "read":
      return { title: str(input.file_path) ?? t("conversation.describe.readFile"), category: "read" };
    case "Write":
      return { title: str(input.file_path) ?? t("conversation.describe.writeFile"), category: "edit" };
    case "Edit":
      return { title: str(input.file_path) ?? t("conversation.describe.editFile"), category: "edit" };
    case "MultiEdit": {
      const path = str(input.file_path);
      const count = Array.isArray(input.edits) ? input.edits.length : 0;
      return {
        title: path ?? t("conversation.describe.editFile"),
        subtitle: count > 1 ? t("conversation.describe.edits", { count }) : undefined,
        category: "edit",
      };
    }
    case "NotebookEdit":
      return { title: str(input.notebook_path) ?? t("conversation.describe.editNotebook"), category: "edit" };
    case "Bash":
    case "GeminiBash": {
      const cmd = commandOf(input);
      // Agents start most commands with `cd <the project> &&`; the summary
      // shows what runs there, and the full command stays in the card.
      const shown = cmd.replace(/^cd\s+("[^"]*"|'[^']*'|\S+)\s*&&\s*/, "");
      return { title: t("conversation.describe.terminal"), subtitle: shown ? truncate(shown, 140) : undefined, category: "terminal" };
    }
    case "exec_command":
    case "shell": {
      const cmd = commandOf(input);
      return { title: cmd ? truncate(cmd, 140) : t("conversation.describe.terminal"), category: "terminal" };
    }
    case "Grep": {
      const pattern = str(input.pattern);
      return {
        title: t("conversation.describe.search"),
        subtitle: pattern ? t("conversation.describe.pattern", { pattern: truncate(pattern, 100) }) : undefined,
        category: "search",
      };
    }
    case "Glob":
      return { title: str(input.pattern) ?? t("conversation.describe.findFiles"), category: "search" };
    case "LS":
      return { title: str(input.path) ? basename(str(input.path)!) : t("conversation.describe.listFiles"), category: "search" };
    case "WebFetch": {
      const url = str(input.url);
      let host = url ?? t("conversation.describe.fetchUrl");
      if (url) {
        try {
          host = new URL(url).hostname;
        } catch {
          /* leave as the raw string */
        }
      }
      return { title: host, category: "web" };
    }
    case "WebSearch": {
      const query = str(input.query);
      return { title: t("conversation.describe.webSearch"), subtitle: query ? truncate(query, 100) : undefined, category: "web" };
    }
    case "TodoWrite": {
      const count = Array.isArray(input.todos) ? input.todos.length : 0;
      return { title: t("conversation.describe.plan"), subtitle: count ? t("conversation.describe.items", { count }) : undefined, category: "todo" };
    }
    case "Task":
    case "Agent": {
      const description = str(input.description) ?? str(input.prompt);
      return { title: description ? truncate(description, 100) : t("conversation.describe.subagentTask"), category: "task" };
    }
    case "ExitPlanMode":
    case "exit_plan_mode":
      return { title: t("conversation.describe.planProposal"), category: "task" };
    case "AskUserQuestion":
    case "request_user_input":
      return { title: t("conversation.describe.question"), category: "question" };
    case "apply_patch":
    case "CodexPatch":
      return { title: t("conversation.describe.applyChanges"), category: "edit" };
    default:
      return { title: name, category: "other" };
  }
}
