// Jupyter notebook (nbformat 4, and the 3.x "worksheets" shape) reduced to
// what the viewer draws: markdown cells, code cells and their outputs.

export type Output =
  | { kind: "text"; text: string; stream?: string }
  | { kind: "error"; text: string }
  | { kind: "image"; mime: string; data: string }
  | { kind: "html"; html: string }
  | { kind: "markdown"; text: string };

export interface Cell {
  type: "markdown" | "code" | "raw";
  source: string;
  count?: number | null;
  outputs: Output[];
}

export interface Notebook {
  language: string;
  cells: Cell[];
}

const joined = (value: unknown): string => (Array.isArray(value) ? value.join("") : typeof value === "string" ? value : "");

// Tracebacks carry terminal colours; the viewer shows plain text.
export const stripAnsi = (text: string) => text.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, "");

function outputsOf(raw: unknown): Output[] {
  if (!Array.isArray(raw)) return [];
  const out: Output[] = [];
  for (const item of raw as Record<string, unknown>[]) {
    const type = item.output_type;
    if (type === "stream") out.push({ kind: "text", text: stripAnsi(joined(item.text)), stream: String(item.name || "stdout") });
    else if (type === "error" || type === "pyerr") {
      const trace = joined(Array.isArray(item.traceback) ? (item.traceback as string[]).join("\n") : "");
      out.push({ kind: "error", text: stripAnsi(trace || `${item.ename}: ${item.evalue}`) });
    } else if (type === "execute_result" || type === "display_data" || type === "pyout") {
      const data = (item.data || item) as Record<string, unknown>;
      const image = ["image/png", "image/jpeg", "image/gif", "image/svg+xml"].find((mime) => data[mime]);
      if (image) {
        const value = joined(data[image]);
        out.push({
          kind: "image",
          mime: image,
          data: image === "image/svg+xml" ? "data:image/svg+xml;charset=utf-8," + encodeURIComponent(value) : `data:${image};base64,${value.replace(/\s+/g, "")}`,
        });
      } else if (data["text/html"]) out.push({ kind: "html", html: joined(data["text/html"]) });
      else if (data["text/markdown"]) out.push({ kind: "markdown", text: joined(data["text/markdown"]) });
      else if (data["text/plain"] || data.text) out.push({ kind: "text", text: stripAnsi(joined(data["text/plain"] ?? data.text)) });
    }
  }
  return out;
}

export function parseNotebook(text: string): Notebook {
  const data = JSON.parse(text) as Record<string, unknown>;
  const metadata = (data.metadata || {}) as Record<string, Record<string, unknown>>;
  const language = String(metadata.kernelspec?.language || metadata.language_info?.name || "python");
  let rawCells = data.cells as Record<string, unknown>[] | undefined;
  if (!rawCells && Array.isArray(data.worksheets)) rawCells = (data.worksheets as Record<string, unknown>[]).flatMap((sheet) => (sheet.cells as Record<string, unknown>[]) || []);
  if (!Array.isArray(rawCells)) throw new Error("This is not a Jupyter notebook: it has no cells.");
  const cells = rawCells.map((cell): Cell => {
    const type = cell.cell_type === "markdown" || cell.cell_type === "heading" ? "markdown" : cell.cell_type === "code" ? "code" : "raw";
    const source = joined(cell.source ?? cell.input);
    return {
      type,
      source: cell.cell_type === "heading" ? "#".repeat(Number(cell.level) || 1) + " " + source : source,
      count: (cell.execution_count ?? cell.prompt_number) as number | null | undefined,
      outputs: type === "code" ? outputsOf(cell.outputs) : [],
    };
  });
  return { language, cells };
}
