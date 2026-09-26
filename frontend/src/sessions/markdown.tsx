// A small, safe assistant-text renderer. No markdown dependency is installed
// in this project (checked frontend/package.json before adding this) and a
// chat transcript's formatting needs are modest — headings, bold/italic,
// inline and fenced code, links, and lists — so a minimal line-based
// renderer is enough. It builds React elements directly, never
// dangerouslySetInnerHTML, so there is no HTML-injection surface: an agent's
// own output (or, via prompt injection, someone else's text quoted into it)
// can contain literal "<script>" and it renders as text, not markup.
import { Fragment, type ReactNode } from "react";

// ---- inline spans: **bold**, *italic*, `code`, [text](url) ----------------

const INLINE = /(\*\*[^*]+\*\*|`[^`]+`|\*[^*]+\*|\[[^\]]+\]\([^)\s]+\))/;

function inline(text: string, keyPrefix: string): ReactNode[] {
  const parts = text.split(INLINE);
  return parts.map((part, i) => {
    const key = `${keyPrefix}-${i}`;
    if (!part) return null;
    if (part.startsWith("**") && part.endsWith("**") && part.length > 3) {
      return <strong key={key}>{part.slice(2, -2)}</strong>;
    }
    if (part.startsWith("`") && part.endsWith("`") && part.length > 1) {
      return <code key={key}>{part.slice(1, -1)}</code>;
    }
    const link = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(part);
    if (link) {
      const label = link[1] ?? "";
      const href = link[2] ?? "";
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
    return <Fragment key={key}>{part}</Fragment>;
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
            return <Tag key={key}>{inline(block.text ?? "", key)}</Tag>;
          }
          case "hr":
            return <hr key={key} />;
          case "ul":
            return (
              <ul key={key}>
                {(block.items ?? []).map((item, j) => (
                  <li key={j}>{inline(item, `${key}-${j}`)}</li>
                ))}
              </ul>
            );
          case "ol":
            return (
              <ol key={key}>
                {(block.items ?? []).map((item, j) => (
                  <li key={j}>{inline(item, `${key}-${j}`)}</li>
                ))}
              </ol>
            );
          default:
            return <p key={key}>{inline(block.text ?? "", key)}</p>;
        }
      })}
    </>
  );
}
