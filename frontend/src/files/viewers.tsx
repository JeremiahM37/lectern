// Rendered views of text files: Markdown (with front matter, a table of
// contents and Mermaid diagrams), HTML in a sandbox, Mermaid files, CSV/TSV
// tables and Jupyter notebooks. Loaded on first use; the terminal page does not
// carry the Markdown parser or the sanitizer until a document needs them.
import DOMPurify from "dompurify";
import { marked, type Token, type Tokens } from "marked";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { detectDelimiter, parseDelimited } from "./csv";
import { headings, splitFrontMatter, wikiLinks } from "./markdown-model";
import { naturalCompare } from "./natsort";
import { parseNotebook, type Output } from "./notebook";

// DOMPurify's default URI rule, plus the internal wiki-link scheme.
const SAFE_URI = /^(?:(?:(?:f|ht)tps?|mailto|tel|lectern-wiki):|[^a-z]|[a-z+.-]+(?:[^a-z+.\-:]|$))/i;
DOMPurify.addHook("afterSanitizeAttributes", (node) => {
  if (node instanceof HTMLAnchorElement && /^(https?:|mailto:)/i.test(node.getAttribute("href") || "")) {
    node.setAttribute("target", "_blank");
    node.setAttribute("rel", "noopener noreferrer");
  }
});
export const sanitize = (html: string) =>
  DOMPurify.sanitize(html, { ALLOWED_URI_REGEXP: SAFE_URI, FORBID_TAGS: ["style", "form", "button", "textarea", "select"], FORBID_ATTR: ["style"] });

interface Block {
  line: number;
  html: string;
}

/** Each top-level block rendered alone, tagged with its first source line. */
function renderBlocks(body: string, firstLine: number): Block[] {
  const tokens = marked.lexer(wikiLinks(body), { gfm: true });
  const blocks: Block[] = [];
  let line = firstLine;
  for (const token of tokens as Token[]) {
    const start = line;
    line += (token.raw.match(/\n/g) || []).length;
    if (token.type === "space") continue;
    let html: string;
    if (token.type === "code" && (token as Tokens.Code).lang?.trim().toLowerCase() === "mermaid")
      // Encoded: the sanitizer drops attribute values containing "-->", which
      // every Mermaid arrow does.
      html = `<div class="wb-mermaid" data-mermaid="${encodeURIComponent((token as Tokens.Code).text)}"></div>`;
    else {
      const list = Object.assign([token], { links: tokens.links });
      html = marked.parser(list, { gfm: true });
    }
    blocks.push({ line: start, html: sanitize(html) });
  }
  return blocks;
}

let mermaidLoad: Promise<typeof import("mermaid").default> | undefined;
let mermaidCount = 0;
/** Draws a Mermaid diagram with scripts, links and HTML labels disabled. */
export async function renderMermaid(source: string): Promise<string> {
  mermaidLoad ??= import("mermaid").then(({ default: mermaid }) => {
    // SVG text labels: HTML labels would need foreignObject, which the
    // sanitizer below removes.
    mermaid.initialize({ startOnLoad: false, securityLevel: "strict", theme: "dark", fontFamily: "system-ui, sans-serif", htmlLabels: false, flowchart: { htmlLabels: false } });
    return mermaid;
  });
  const mermaid = await mermaidLoad;
  const { svg } = await mermaid.render("wb-mermaid-" + ++mermaidCount, source);
  return DOMPurify.sanitize(svg, { USE_PROFILES: { svg: true, svgFilters: true } });
}

function useMermaid(container: React.RefObject<HTMLElement | null>, revision: unknown) {
  useEffect(() => {
    let stopped = false;
    for (const node of container.current?.querySelectorAll<HTMLElement>(".wb-mermaid:not([data-drawn])") || []) {
      node.dataset.drawn = "1";
      node.textContent = "Drawing diagram…";
      void renderMermaid(decodeURIComponent(node.dataset.mermaid || ""))
        .then((svg) => {
          if (!stopped) node.innerHTML = svg;
        })
        .catch((error: unknown) => {
          if (stopped) return;
          node.classList.add("error");
          node.textContent = "Diagram error: " + (error instanceof Error ? error.message : String(error));
        });
    }
    return () => {
      stopped = true;
    };
  }, [revision]);
}

export interface MarkdownProps {
  text: string;
  dir: string; // the document's folder, for relative links and images
  syncLine?: number;
  toc: boolean;
  onOpenPath: (path: string, anchor?: string) => void;
  onWiki: (name: string, anchor?: string) => void;
  onSourceLine?: (line: number) => void;
  loadImage: (path: string) => Promise<string>;
}

function resolvePath(dir: string, href: string): string | undefined {
  const parts = href.startsWith("/") ? [] : dir.split("/").filter((part) => part && part !== ".");
  for (const part of href.split("/")) {
    if (part === "..") {
      if (!parts.length) return undefined;
      parts.pop();
    } else if (part && part !== ".") parts.push(part);
  }
  return parts.join("/") || undefined;
}

export function MarkdownView(props: MarkdownProps) {
  const front = useMemo(() => splitFrontMatter(props.text), [props.text]);
  const blocks = useMemo(() => renderBlocks(front.body, front.lines + 1), [front]);
  const toc = useMemo(() => headings(front.body), [front]);
  const root = useRef<HTMLDivElement>(null);
  const [tocOpen, setTocOpen] = useState(false);
  useMermaid(root, blocks);
  // Heading anchors follow the same slugs as the table of contents.
  useEffect(() => {
    const nodes = root.current?.querySelectorAll<HTMLElement>(".wb-md-block h1,.wb-md-block h2,.wb-md-block h3,.wb-md-block h4,.wb-md-block h5,.wb-md-block h6");
    nodes?.forEach((node, index) => {
      if (toc[index]) node.id = "wb-h-" + toc[index]!.slug;
    });
  }, [blocks, toc]);
  // Relative images are workspace files: fetch them through the file API.
  useEffect(() => {
    const urls: string[] = [];
    let stopped = false;
    for (const image of root.current?.querySelectorAll<HTMLImageElement>("img[src]") || []) {
      const src = image.getAttribute("src") || "";
      if (/^(?:[a-z]+:|\/\/|#)/i.test(src)) continue;
      const path = resolvePath(props.dir, decodeURIComponent(src.split(/[?#]/)[0]!));
      image.removeAttribute("src");
      if (!path) continue;
      void props.loadImage(path).then(
        (url) => {
          if (stopped) URL.revokeObjectURL(url);
          else {
            urls.push(url);
            image.src = url;
          }
        },
        () => image.classList.add("wb-missing"),
      );
    }
    return () => {
      stopped = true;
      urls.forEach((url) => URL.revokeObjectURL(url));
    };
  }, [blocks, props.dir]);
  useEffect(() => {
    if (!props.syncLine || !root.current) return;
    let best: HTMLElement | undefined;
    for (const node of root.current.querySelectorAll<HTMLElement>("[data-line]")) {
      if (Number(node.dataset.line) <= props.syncLine) best = node;
      else break;
    }
    const scroller = root.current.closest(".wb-md-scroll");
    if (best && scroller) scroller.scrollTop = best.offsetTop - 8;
  }, [props.syncLine]);
  const jump = (slug: string) => {
    root.current?.querySelector("#wb-h-" + CSS.escape(slug))?.scrollIntoView({ block: "start" });
    setTocOpen(false);
  };
  return (
    <div className={"wb-md" + (props.toc && toc.length > 1 ? " with-toc" : "")}>
      {props.toc && toc.length > 1 && (
        <nav className={"wb-toc" + (tocOpen ? " open" : "")} aria-label="Contents">
          <button className="wb-toc-toggle" aria-expanded={tocOpen} onClick={() => setTocOpen(!tocOpen)}>
            Contents
          </button>
          <ol>
            {toc.map((heading) => (
              <li key={heading.slug} style={{ paddingLeft: (heading.level - 1) * 12 }}>
                <a
                  href={"#" + heading.slug}
                  onClick={(event) => {
                    event.preventDefault();
                    jump(heading.slug);
                  }}
                >
                  {heading.text.replace(/[`*_]/g, "")}
                </a>
              </li>
            ))}
          </ol>
        </nav>
      )}
      <article
        ref={root}
        className="wb-md-body"
        onClick={(event) => {
          const anchor = (event.target as Element).closest("a");
          if (anchor) {
            const href = anchor.getAttribute("href") || "";
            if (/^(https?:|mailto:)/i.test(href)) return;
            event.preventDefault();
            if (href.startsWith("#")) jump(decodeURIComponent(href.slice(1)).toLowerCase());
            else if (href.startsWith("lectern-wiki:")) {
              const [name, anchorName] = href.slice("lectern-wiki:".length).split("#");
              props.onWiki(decodeURIComponent(name!), anchorName && decodeURIComponent(anchorName));
            } else {
              const [target, hash] = href.split("#");
              const path = resolvePath(props.dir, decodeURIComponent(target!));
              if (path) props.onOpenPath(path, hash);
            }
            return;
          }
          if (!props.onSourceLine || window.getSelection()?.toString()) return;
          const block = (event.target as Element).closest<HTMLElement>("[data-line]");
          if (block) props.onSourceLine(Number(block.dataset.line));
        }}
      >
        {front.fields.length > 0 && (
          <table className="wb-front-matter" data-line={1}>
            <caption>Front matter</caption>
            <tbody>
              {front.fields.map(([key, value]) => (
                <tr key={key}>
                  <th>{key}</th>
                  <td>{value}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {blocks.map((block, index) => (
          <div key={index + ":" + block.line} className="wb-md-block" data-line={block.line} dangerouslySetInnerHTML={{ __html: block.html }} />
        ))}
        {!blocks.length && !front.fields.length && <p className="wb-note">This document is empty.</p>}
      </article>
    </div>
  );
}

/**
 * HTML is shown in an opaque-origin sandbox. Scripts stay off unless asked
 * for; even then a content policy blocks every network request, so a page
 * cannot reach Lectern's API or anything else.
 */
export function HtmlView({ text, scripts }: { text: string; scripts: boolean }) {
  const policy = scripts
    ? "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:"
    : "default-src 'none'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:";
  const document = `<!doctype html><meta http-equiv="Content-Security-Policy" content="${policy}">` + text;
  return <iframe className="wb-html" title="HTML preview" sandbox={scripts ? "allow-scripts" : ""} srcDoc={document} referrerPolicy="no-referrer" />;
}

export function MermaidView({ text }: { text: string }) {
  const [svg, setSvg] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    let stopped = false;
    setError("");
    void renderMermaid(text).then(
      (value) => !stopped && setSvg(value),
      (reason: unknown) => !stopped && setError(reason instanceof Error ? reason.message : String(reason)),
    );
    return () => {
      stopped = true;
    };
  }, [text]);
  if (error) return <p className="wb-error" role="alert">Diagram error: {error}</p>;
  return svg ? <div className="wb-mermaid-file" dangerouslySetInnerHTML={{ __html: svg }} /> : <p className="wb-note">Drawing diagram…</p>;
}

const ROW_LIMIT = 5000;
export function CsvTable({ text, path }: { text: string; path: string }) {
  const parsed = useMemo(() => parseDelimited(text, detectDelimiter(path, text), ROW_LIMIT + 1), [text, path]);
  const [filter, setFilter] = useState("");
  const [sort, setSort] = useState<{ column: number; descending: boolean }>();
  const [header = [], ...rows] = parsed.rows;
  const shown = useMemo(() => {
    const needle = filter.toLowerCase();
    let list = rows.map((row, index) => ({ row, index }));
    if (needle) list = list.filter(({ row }) => row.some((cell) => cell.toLowerCase().includes(needle)));
    if (sort)
      list.sort((a, b) => {
        const order = naturalCompare(a.row[sort.column] || "", b.row[sort.column] || "");
        return sort.descending ? -order : order;
      });
    return list;
  }, [rows, filter, sort]);
  const width = Math.max(header.length, ...rows.slice(0, 200).map((row) => row.length));
  return (
    <div className="wb-table">
      <div className="wb-table-tools">
        <input type="search" placeholder="Filter rows" aria-label="Filter rows" value={filter} onChange={(event) => setFilter(event.target.value)} />
        <span>
          {shown.length.toLocaleString()} of {rows.length.toLocaleString()} rows
          {parsed.truncated || rows.length > ROW_LIMIT ? ` (first ${ROW_LIMIT.toLocaleString()} shown)` : ""}
        </span>
      </div>
      <div className="wb-table-scroll">
        <table>
          <thead>
            <tr>
              <th className="wb-row-number">#</th>
              {Array.from({ length: width }, (_, column) => (
                <th key={column} aria-sort={sort?.column === column ? (sort.descending ? "descending" : "ascending") : undefined}>
                  <button onClick={() => setSort(sort?.column === column ? (sort.descending ? undefined : { column, descending: true }) : { column, descending: false })}>
                    {header[column] ?? `Column ${column + 1}`}
                    {sort?.column === column ? (sort.descending ? " ↓" : " ↑") : ""}
                  </button>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {shown.slice(0, ROW_LIMIT).map(({ row, index }) => (
              <tr key={index}>
                <td className="wb-row-number">{index + 1}</td>
                {Array.from({ length: width }, (_, column) => (
                  <td key={column}>{row[column] ?? ""}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function OutputView({ output, markdown }: { output: Output; markdown: (text: string) => ReactNode }) {
  switch (output.kind) {
    case "text":
      return <pre className={"wb-nb-output" + (output.stream === "stderr" ? " stderr" : "")}>{output.text}</pre>;
    case "error":
      return <pre className="wb-nb-output error">{output.text}</pre>;
    case "image":
      return <img className="wb-nb-image" src={output.data} alt="Cell output" />;
    case "html":
      return <div className="wb-nb-html" dangerouslySetInnerHTML={{ __html: sanitize(output.html) }} />;
    case "markdown":
      return <>{markdown(output.text)}</>;
  }
}

export function NotebookView({ text, markdown }: { text: string; markdown: (text: string) => ReactNode }) {
  const parsed = useMemo(() => {
    try {
      return { notebook: parseNotebook(text) };
    } catch (error) {
      return { error: error instanceof Error ? error.message : String(error) };
    }
  }, [text]);
  if (!parsed.notebook) return <p className="wb-error" role="alert">{parsed.error}</p>;
  return (
    <div className="wb-notebook">
      {parsed.notebook.cells.map((cell, index) => (
        <section key={index} className={"wb-nb-cell " + cell.type}>
          {cell.type === "markdown" ? (
            markdown(cell.source)
          ) : (
            <>
              <div className="wb-nb-input">
                <span className="wb-nb-prompt">{cell.type === "code" ? `[${cell.count ?? " "}]` : ""}</span>
                <pre data-language={parsed.notebook.language}>{cell.source}</pre>
              </div>
              {cell.outputs.map((output, outputIndex) => (
                <OutputView key={outputIndex} output={output} markdown={markdown} />
              ))}
            </>
          )}
        </section>
      ))}
    </div>
  );
}

/** A small Markdown render for notebook cells: no TOC, no source mapping. */
export function MarkdownFragment({ text }: { text: string }) {
  const root = useRef<HTMLDivElement>(null);
  const html = useMemo(() => renderBlocks(text, 1).map((block) => block.html).join(""), [text]);
  useMermaid(root, html);
  return <div ref={root} className="wb-md-body wb-md-fragment" dangerouslySetInnerHTML={{ __html: html }} />;
}
