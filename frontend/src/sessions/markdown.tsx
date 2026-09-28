// A small, safe assistant-text renderer. No markdown dependency is installed
// in this project (checked frontend/package.json before adding this) and a
// chat transcript's formatting needs are modest — headings, bold/italic,
// inline and fenced code, links, and lists — so a minimal line-based
// renderer is enough. It builds React elements directly, never
// dangerouslySetInnerHTML, so there is no HTML-injection surface: an agent's
// own output (or, via prompt injection, someone else's text quoted into it)
// can contain literal "<script>" and it renders as text, not markup.
import { createContext, Fragment, useContext, useState, type ReactNode } from "react";
import { hyperlinkTarget, linksOnRow, type TerminalLink } from "../terminal/links";

// ---- paths and links in agent messages ------------------------------------
//
// A path an agent writes (an absolute one, one in the workspace, a Markdown
// link to a file) opens the way a click in the web terminal does: workspace
// files in the viewer, others read-only, web addresses in a new tab
// (docs/files.md). The chat that renders this supplies how; without it,
// paths stay text.

export interface FileLinks {
  workdir: string;
  /** Whether a bare workspace name names a file: only then is it a link. */
  exists(path: string): boolean | Promise<boolean>;
  open(link: TerminalLink): void;
}

export const FileLinksContext = createContext<FileLinks | null>(null);

function FileRef({ link, children }: { link: TerminalLink; children: ReactNode }) {
  const links = useContext(FileLinksContext);
  const known = link.kind === "file" && link.verify ? links?.exists(link.path) : true;
  const [exists, setExists] = useState<boolean | undefined>(typeof known === "boolean" ? known : undefined);
  if (!links) return <>{children}</>;
  const check = () => {
    if (exists !== undefined || link.kind !== "file" || !link.verify) return;
    void Promise.resolve(links.exists(link.path)).then(setExists, () => setExists(false));
  };
  if (exists === false) return <>{children}</>;
  const title = link.kind === "url" ? link.url : link.path;
  return (
    <a
      className={"md-file-link" + (exists === undefined ? " unverified" : "")}
      href={link.kind === "url" ? link.url : "#"}
      target={link.kind === "url" ? "_blank" : undefined}
      rel="noreferrer noopener"
      title={title}
      data-path={link.kind === "file" ? link.path : undefined}
      onMouseEnter={check}
      onFocus={check}
      onClick={(event) => {
        event.preventDefault();
        links.open(link);
      }}
    >
      {children}
    </a>
  );
}

// Plain text with the paths and web addresses in it made into links.
function linkify(text: string, key: string, workdir: string): ReactNode[] {
  const out: ReactNode[] = [];
  text.split("\n").forEach((line, n) => {
    if (n > 0) out.push("\n");
    let at = 0;
    const found = linksOnRow([{ text: line }], 0, workdir).sort((a, b) => a.spans![0]!.start - b.spans![0]!.start);
    for (const [i, link] of found.entries()) {
      const { start, end } = link.spans![0]!;
      if (start < at) continue;
      if (start > at) out.push(line.slice(at, start));
      out.push(
        <FileRef key={`${key}-l${n}-${i}`} link={link}>
          {line.slice(start, end)}
        </FileRef>,
      );
      at = end;
    }
    if (at < line.length) out.push(line.slice(at));
  });
  return out;
}

// ---- inline spans: **bold**, *italic*, `code`, [text](url) ----------------

const INLINE = /(\*\*[^*]+\*\*|`[^`]+`|\*[^*]+\*|\[[^\]]+\]\([^)\s]+\))/;

function inline(text: string, keyPrefix: string, workdir: string | null): ReactNode[] {
  const parts = text.split(INLINE);
  return parts.map((part, i) => {
    const key = `${keyPrefix}-${i}`;
    if (!part) return null;
    if (part.startsWith("**") && part.endsWith("**") && part.length > 3) {
      return <strong key={key}>{part.slice(2, -2)}</strong>;
    }
    if (part.startsWith("`") && part.endsWith("`") && part.length > 1) {
      const code = part.slice(1, -1);
      // A path in backticks, as agents often write one, is a link as a whole.
      const whole = workdir !== null ? linksOnRow([{ text: code }], 0, workdir).find((l) => l.spans![0]!.start === 0 && l.spans![0]!.end === code.length) : undefined;
      if (whole)
        return (
          <FileRef key={key} link={whole}>
            <code>{code}</code>
          </FileRef>
        );
      return <code key={key}>{code}</code>;
    }
    const link = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(part);
    if (link) {
      const label = link[1] ?? "";
      const href = link[2] ?? "";
      // A Markdown link to a file: file://, an absolute or ~/ path, or a
      // path in the workspace.
      const file = workdir !== null && !/^https?:\/\//i.test(href) ? (hyperlinkTarget(href, workdir) ?? linksOnRow([{ text: href }], 0, workdir).find((l) => l.kind === "file" && l.spans![0]!.start === 0)) : undefined;
      if (file && file.kind === "file")
        return (
          <FileRef key={key} link={{ ...file, verify: false }}>
            {label}
          </FileRef>
        );
      // Only ever a real link scheme — never javascript:/data: from agent
      // output or (via prompt injection) quoted third-party text.
      if (/^https?:\/\//i.test(href)) {
        return (
          <a key={key} href={href} target="_blank" rel="noreferrer noopener">
            {label}
          </a>
        );
      }
      return <Fragment key={key}>{part}</Fragment>;
    }
    if (part.startsWith("*") && part.endsWith("*") && part.length > 1) {
      return <em key={key}>{part.slice(1, -1)}</em>;
    }
    return <Fragment key={key}>{workdir !== null ? linkify(part, key, workdir) : part}</Fragment>;
  });
}

// ---- block structure: fenced code, headings, lists, paragraphs -----------

interface Block {
  type: "code" | "heading" | "ul" | "ol" | "p" | "hr";
  text?: string;
  lang?: string;
  level?: number;
  items?: string[];
}

function blocksOf(markdown: string): Block[] {
  const lines = markdown.replace(/\r\n/g, "\n").split("\n");
  const at = (idx: number): string => lines[idx] ?? "";
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = at(i);
    const fence = /^```(\w*)\s*$/.exec(line);
    if (fence) {
      const lang = fence[1] || undefined;
      const body: string[] = [];
      i++;
      while (i < lines.length && !/^```\s*$/.test(at(i))) {
        body.push(at(i));
        i++;
      }
      i++; // consume the closing fence
      blocks.push({ type: "code", text: body.join("\n"), lang });
      continue;
    }
    if (/^\s*$/.test(line)) {
      i++;
      continue;
    }
    if (/^---+\s*$/.test(line)) {
      blocks.push({ type: "hr" });
      i++;
      continue;
    }
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      blocks.push({ type: "heading", level: (heading[1] ?? "#").length, text: heading[2] ?? "" });
      i++;
      continue;
    }
    const bullet = /^\s*[-*]\s+(.*)$/.exec(line);
    if (bullet) {
      const items: string[] = [];
      while (i < lines.length) {
        const m = /^\s*[-*]\s+(.*)$/.exec(at(i));
        if (!m) break;
        items.push(m[1] ?? "");
        i++;
      }
      blocks.push({ type: "ul", items });
      continue;
    }
    const numbered = /^\s*\d+[.)]\s+(.*)$/.exec(line);
    if (numbered) {
      const items: string[] = [];
      while (i < lines.length) {
        const m = /^\s*\d+[.)]\s+(.*)$/.exec(at(i));
        if (!m) break;
        items.push(m[1] ?? "");
        i++;
      }
      blocks.push({ type: "ol", items });
      continue;
    }
    // A paragraph runs until a blank line or the start of another block.
    const para: string[] = [line];
    i++;
    while (
      i < lines.length &&
      !/^\s*$/.test(at(i)) &&
      !/^```/.test(at(i)) &&
      !/^(#{1,6})\s+/.test(at(i)) &&
      !/^\s*[-*]\s+/.test(at(i)) &&
      !/^\s*\d+[.)]\s+/.test(at(i))
    ) {
      para.push(at(i));
      i++;
    }
    blocks.push({ type: "p", text: para.join("\n") });
  }
  return blocks;
}

/** Renders a modest markdown subset (headings, bold/italic, inline+fenced
 * code, links, lists) as React elements — no HTML parsing, so nothing in
 * the source string is ever interpreted as markup. */
export function Markdown({ text }: { text: string }) {
  const blocks = blocksOf(text);
  const workdir = useContext(FileLinksContext)?.workdir ?? null;
  return (
    <>
      {blocks.map((block, i) => {
        const key = `b${i}`;
        switch (block.type) {
          case "code":
            return (
              <pre key={key} className="md-code">
                <code>{block.text}</code>
              </pre>
            );
          case "heading": {
            const Tag = (`h${Math.min(6, (block.level ?? 1) + 2)}` as unknown) as "h3";
            return <Tag key={key}>{inline(block.text ?? "", key, workdir)}</Tag>;
          }
          case "hr":
            return <hr key={key} />;
          case "ul":
            return (
              <ul key={key}>
                {(block.items ?? []).map((item, j) => (
                  <li key={j}>{inline(item, `${key}-${j}`, workdir)}</li>
                ))}
              </ul>
            );
          case "ol":
            return (
              <ol key={key}>
                {(block.items ?? []).map((item, j) => (
                  <li key={j}>{inline(item, `${key}-${j}`, workdir)}</li>
                ))}
              </ol>
            );
          default:
            return <p key={key}>{inline(block.text ?? "", key, workdir)}</p>;
        }
      })}
    </>
  );
}
