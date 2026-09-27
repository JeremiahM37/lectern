import { Fragment, useMemo, useState, type ReactNode } from "react";
import { commentTarget, type DiffLine } from "./diffLines";
import {
  anchorFor,
  attributionCounts,
  authorOf,
  fingerprint,
  isImagePath,
  parseFile,
  splitRows,
  type CommentState,
  type DiffMode,
  type FileAttribution,
  type Hunk,
  type ParsedFile,
} from "./diffModel";
import { ImageDiff, type ImageSource } from "./ImageDiff";
import type { DraftComment, FilePatch, FileStat } from "./types";

/** A comment shown inline under its line. Drafts from the task view carry no
 * state; stored session comments carry where they stand after the agent's
 * edits (see diffModel.commentState). */
export interface LineNote extends DraftComment {
  state?: CommentState;
  round?: number;
}

export interface DiffViewerProps {
  repoLabel?: string;
  files: FilePatch[];
  stats: FileStat[];
  wrap: boolean;
  truncated?: boolean;
  commentable?: boolean;
  comments?: LineNote[];
  onAddComment?(c: Omit<DraftComment, "key">): void;
  /** Unified (default) or side by side. */
  mode?: DiffMode;
  /** Who wrote each added line, per file path. */
  attribution?: Record<string, FileAttribution>;
  /** Fingerprints of the files marked viewed; a viewed file starts collapsed. */
  viewed?: Record<string, string>;
  onToggleViewed?(path: string, fingerprint: string, viewed: boolean): void;
  /** Loads one side of an image; without it images show as text. */
  imageSource?: ImageSource;
  /** Buttons under an inline note (resolve, reopen, delete). */
  noteActions?(note: LineNote): ReactNode;
  /** Buttons in a hunk's header row (stage, unstage, discard). */
  hunkActions?(file: FilePatch, hunk: Hunk): ReactNode;
  /** Buttons in a file's header (stage, unstage, discard). */
  fileActions?(file: FilePatch): ReactNode;
  /** Checkboxes on added and removed lines, for staging chosen lines. The
   * line index counts the hunk's body lines, as the server's hunk action
   * does. */
  lineChecks?: {
    checked(path: string, hunk: number, line: number): boolean;
    toggle(path: string, hunk: number, line: number): void;
  };
  /** Prefix for element ids, so two viewers on one page never collide. */
  idPrefix?: string;
}

const STATE_LABEL: Record<CommentState, string> = {
  draft: "Draft",
  open: "Sent · not changed yet",
  addressed: "Agent changed this",
  resolved: "Resolved",
};

type Target = { file: string; line: number; side: "old" | "new"; code: string };

function lineClass(line: DiffLine | undefined): string {
  switch (line?.kind) {
    case "add":
      return "dl-add";
    case "del":
      return "dl-del";
    case "hunk":
      return "dl-hunk";
    case "meta":
      return "dl-meta";
    default:
      return line ? "dl-ctx" : "dl-empty";
  }
}

function countLines(file: ParsedFile): FileStat {
  let additions = 0,
    deletions = 0;
  for (const h of file.hunks)
    for (const l of h.lines) {
      if (l.kind === "add") additions++;
      if (l.kind === "del") deletions++;
    }
  return { path: "", additions, deletions };
}

export function fileDomId(prefix: string, path: string): string {
  return `${prefix}file-${fingerprint(path)}`;
}

/**
 * Renders one or more files' diffs, unified or side by side, with real line
 * numbers, an authorship gutter, inline comments and optional hunk/file
 * actions. When `commentable`, a click (or tap) on any line opens an inline
 * composer. Works on a phone: the composer is a normal block in the flow,
 * reached by tap like any other control.
 */
export function DiffViewer({
  repoLabel,
  files,
  stats,
  wrap,
  truncated,
  commentable,
  comments,
  onAddComment,
  mode = "unified",
  attribution,
  viewed,
  onToggleViewed,
  imageSource,
  noteActions,
  hunkActions,
  fileActions,
  lineChecks,
  idPrefix = "",
}: DiffViewerProps) {
  const [active, setActive] = useState<Target>();
  const [draftText, setDraftText] = useState("");
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [textForImage, setTextForImage] = useState<Record<string, boolean>>({});
  const parsed = useMemo(
    () => new Map(files.map((f) => [f.path, parseFile(f.patch)] as const)),
    [files],
  );

  function submit(file: ParsedFile) {
    if (!active || !draftText.trim() || !onAddComment) return;
    const anchor = anchorFor(file, active.side, active.line);
    onAddComment({
      ...active,
      text: draftText.trim(),
      context_before: anchor?.context_before ?? "",
      context_after: anchor?.context_after ?? "",
    });
    setActive(undefined);
    setDraftText("");
  }

  const notesAt = (path: string, t: { line: number; side: string } | undefined) =>
    t ? (comments ?? []).filter((c) => c.file === path && c.line === t.line && c.side === t.side) : [];

  function composer(file: ParsedFile) {
    return (
      <div className="dl-composer">
        <textarea
          autoFocus
          rows={2}
          placeholder="Leave a comment on this line…"
          value={draftText}
          onChange={(e) => setDraftText(e.target.value)}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
              e.preventDefault();
              submit(file);
            }
            if (e.key === "Escape") {
              e.stopPropagation();
              setActive(undefined);
            }
          }}
        />
        <div className="dl-composer-actions">
          <button type="button" className="b" onClick={() => setActive(undefined)}>
            Cancel
          </button>
          <button
            type="button"
            className="b ok"
            disabled={!draftText.trim()}
            onClick={() => submit(file)}
          >
            Add comment
          </button>
        </div>
      </div>
    );
  }

  function notes(list: LineNote[]) {
    if (!list.length) return null;
    return (
      <div className="dl-notes">
        {list.map((n) => (
          <div key={n.key} className="dl-note" data-state={n.state ?? "draft"}>
            <div className="dl-note-head">
              <span className={`dl-note-state s-${n.state ?? "draft"}`}>
                {STATE_LABEL[n.state ?? "draft"]}
                {n.round ? ` · round ${n.round}` : ""}
              </span>
              {noteActions?.(n)}
            </div>
            <p>{n.text}</p>
          </div>
        ))}
      </div>
    );
  }

  function isOpen(path: string, t: Omit<Target, "file"> | undefined) {
    return !!(active && t && active.file === path && active.line === t.line && active.side === t.side);
  }

  function gutter(line: DiffLine | undefined, path: string, which: "both" | "old" | "new") {
    const author =
      line?.kind === "add" && line.newLine !== undefined
        ? authorOf(attribution?.[path], line.newLine)
        : undefined;
    return (
      <>
        {which !== "new" && <span className="dl-num">{line?.oldLine ?? ""}</span>}
        {which !== "old" && <span className="dl-num">{line?.newLine ?? ""}</span>}
        {attribution && (
          <span
            className={"dl-auth" + (author ? ` dl-auth-${author}` : "")}
            title={
              author === "agent" ? "Written by the agent" : author === "human" ? "Written by you" : undefined
            }
            aria-label={author === "agent" ? "agent" : author === "human" ? "you" : undefined}
          />
        )}
      </>
    );
  }

  function unifiedHunk(f: FilePatch, file: ParsedFile, h: Hunk, fileIndex: number) {
    return h.lines.map((line, i) => {
      const target = commentable ? commentTarget(line) : undefined;
      const here = target ? notesAt(f.path, target) : [];
      const checkable = lineChecks && (line.kind === "add" || line.kind === "del");
      const checked = checkable && lineChecks.checked(f.path, h.index, i);
      return (
        <Fragment key={`${h.index}-${i}`}>
          <div
            className={`dl-row ${lineClass(line)}${target || checkable ? " dl-commentable" : ""}${checked ? " dl-checked" : ""}`}
            data-file-index={fileIndex}
            onClick={() => {
              if (checkable) {
                lineChecks.toggle(f.path, h.index, i);
                return;
              }
              if (!target) return;
              setActive({ file: f.path, ...target });
              setDraftText("");
            }}
          >
            {lineChecks && (
              <span className="dl-check">
                {checkable && (
                  <input
                    type="checkbox"
                    aria-label={`Choose line ${line.newLine ?? line.oldLine}`}
                    checked={!!checked}
                    onClick={(e) => e.stopPropagation()}
                    onChange={() => lineChecks.toggle(f.path, h.index, i)}
                  />
                )}
              </span>
            )}
            {gutter(line, f.path, "both")}
            <span className="dl-text">{line.text || " "}</span>
            {here.length > 0 && (
              <span className="dl-comment-badge" title={`${here.length} comment(s) on this line`}>
                💬 {here.length}
              </span>
            )}
          </div>
          {notes(here)}
          {isOpen(f.path, target) && composer(file)}
        </Fragment>
      );
    });
  }

  function splitHunk(f: FilePatch, file: ParsedFile, h: Hunk) {
    return splitRows(h.lines).map((row, i) => {
      const halves = (["old", "new"] as const).map((side) => {
        const line = side === "old" ? row.left : row.right;
        let target: Target | undefined;
        if (commentable && line && line.kind !== "meta") {
          const n = side === "old" ? line.oldLine : line.newLine;
          if (n !== undefined) target = { file: f.path, line: n, side, code: line.text.slice(1) };
        }
        return { side, line, target };
      });
      const rowNotes = halves.flatMap((x) => notesAt(f.path, x.target));
      const openHere = halves.find((x) => isOpen(f.path, x.target));
      return (
        <Fragment key={`${h.index}-${i}`}>
          <div className="dl-split">
            {halves.map(({ side, line, target }) => (
              <div
                key={side}
                className={`dl-half ${line ? lineClass(line) : "dl-empty"}${target ? " dl-commentable" : ""}`}
                data-side={side}
                onClick={() => {
                  if (!target) return;
                  setActive(target);
                  setDraftText("");
                }}
              >
                {gutter(line, f.path, side)}
                <span className="dl-text">{line ? line.text.slice(1) || " " : ""}</span>
                {target && notesAt(f.path, target).length > 0 && (
                  <span className="dl-comment-badge">💬 {notesAt(f.path, target).length}</span>
                )}
              </div>
            ))}
          </div>
          {notes(rowNotes)}
          {openHere && composer(file)}
        </Fragment>
      );
    });
  }

  return (
    <div className={`diff${wrap ? " wrapped" : ""} diff-${mode}`}>
      {repoLabel && <p className="sub review-repo-label">{repoLabel}</p>}
      {truncated && (
        <p className="sub review-truncated">
          This diff was too large to show in full and has been truncated.
        </p>
      )}
      {files.length === 0 && <p className="sub">No changes.</p>}
      {files.map((f, fileIndex) => {
        const file = parsed.get(f.path) ?? parseFile(f.patch);
        const s = stats.find((x) => x.path === f.path) ?? countLines(file);
        const fp = fingerprint(f.patch);
        const isViewed = viewed?.[f.path] === fp;
        const expanded = open[f.path] ?? (!isViewed && files.length <= 30);
        const counts = attribution ? attributionCounts(attribution[f.path]) : undefined;
        const image = imageSource && isImagePath(f.path) && !textForImage[f.path];
        const notable = file.meta.filter((m) =>
          /^(new file|deleted file|rename |similarity|old mode|new mode|Binary files)/.test(m.text),
        );
        return (
          <details
            className={"dfile" + (isViewed ? " dfile-viewed" : "")}
            open={expanded}
            key={f.path}
            id={fileDomId(idPrefix, f.path)}
            data-path={f.path}
            onToggle={(e) => {
              const now = e.currentTarget.open;
              if (now !== expanded) setOpen((o) => ({ ...o, [f.path]: now }));
            }}
          >
            <summary>
              <span className="dfile-path">{f.path}</span>
              {counts && (counts.agent > 0 || counts.human > 0) && (
                <span className="dfile-authors" title="Added lines by author">
                  {counts.agent > 0 && <span className="a-agent">◆ {counts.agent}</span>}
                  {counts.human > 0 && <span className="a-human">● {counts.human}</span>}
                </span>
              )}
              <span className="pm">
                <b className="a">+{s?.additions ?? "?"}</b> <b className="d">−{s?.deletions ?? "?"}</b>
              </span>
              {fileActions && (
                <span className="dfile-actions" onClick={(e) => e.preventDefault()}>
                  {fileActions(f)}
                </span>
              )}
              {onToggleViewed && (
                <label className="dfile-viewed-toggle" onClick={(e) => e.stopPropagation()}>
                  <input
                    type="checkbox"
                    checked={isViewed}
                    onChange={(e) => {
                      onToggleViewed(f.path, fp, e.target.checked);
                      setOpen((o) => ({ ...o, [f.path]: !e.target.checked }));
                    }}
                  />
                  Viewed
                </label>
              )}
            </summary>
            {expanded && (
              <div className="dcode">
                {notable.map((m, i) => (
                  <div key={`m${i}`} className="dl-row dl-meta">
                    <span className="dl-text">{m.text}</span>
                  </div>
                ))}
                {image ? (
                  <>
                    <ImageDiff path={f.path} source={imageSource} />
                    {file.hunks.length > 0 && (
                      <button
                        type="button"
                        className="b img-diff-text"
                        onClick={() => setTextForImage((o) => ({ ...o, [f.path]: true }))}
                      >
                        Show as text
                      </button>
                    )}
                  </>
                ) : (
                  file.hunks.map((h) => (
                    <Fragment key={h.index}>
                      <div
                        className="dl-row dl-hunk"
                        data-hunk={`${fileIndex}:${h.index}`}
                        data-path={f.path}
                      >
                        <span className="dl-text">{h.header.text}</span>
                        {hunkActions && <span className="dl-hunk-actions">{hunkActions(f, h)}</span>}
                      </div>
                      {mode === "split" ? splitHunk(f, file, h) : unifiedHunk(f, file, h, fileIndex)}
                    </Fragment>
                  ))
                )}
                {!image && file.hunks.length === 0 && !notable.length && (
                  <div className="dl-row dl-meta">
                    <span className="dl-text">No textual changes.</span>
                  </div>
                )}
              </div>
            )}
          </details>
        );
      })}
    </div>
  );
}
