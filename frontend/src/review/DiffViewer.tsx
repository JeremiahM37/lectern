import { Fragment, useState } from "react";
import { commentTarget, parsePatchLines } from "./diffLines";
import type { DraftComment, FilePatch, FileStat } from "./types";

/**
 * Renders one or more repositories' diffs using the same classes the task
 * diff view already ships (web/static/style.css: .diff/.dfile/.dcode/
 * .dl-add/.dl-del/.dl-hunk), and — when `commentable` — lets a click on any
 * changed line open a small inline composer that adds a draft comment. Works
 * on a phone: the composer is a normal block in the flow (no positioned
 * popover to fit on a 390px screen), reached by tap like any other control.
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
}: {
  repoLabel?: string;
  files: FilePatch[];
  stats: FileStat[];
  wrap: boolean;
  truncated?: boolean;
  commentable?: boolean;
  comments?: DraftComment[];
  onAddComment?(c: Omit<DraftComment, "key">): void;
}) {
  const [active, setActive] = useState<{
    file: string;
    line: number;
    side: "old" | "new";
    code: string;
  }>();
  const [draftText, setDraftText] = useState("");

  function submit() {
    if (!active || !draftText.trim() || !onAddComment) return;
    onAddComment({ ...active, text: draftText.trim() });
    setActive(undefined);
    setDraftText("");
  }

  return (
    <div className={wrap ? "diff wrapped" : "diff"}>
      {repoLabel && <p className="sub review-repo-label">{repoLabel}</p>}
      {truncated && (
        <p className="sub review-truncated">
          This diff was too large to show in full and has been truncated.
        </p>
      )}
      {files.length === 0 && <p className="sub">No changes.</p>}
      {files.map((f) => {
        const s = stats.find((x) => x.path === f.path);
        const lines = parsePatchLines(f.patch);
        return (
          <details className="dfile" open={files.length <= 3} key={f.path}>
            <summary>
              {f.path}{" "}
              <span className="pm">
                <b className="a">+{s?.additions ?? "?"}</b>{" "}
                <b className="d">−{s?.deletions ?? "?"}</b>
              </span>
            </summary>
            <div className="dcode">
              {lines.map((line, i) => {
                const target = commentable ? commentTarget(line) : undefined;
                const isOpen =
                  active &&
                  active.file === f.path &&
                  target &&
                  active.line === target.line &&
                  active.side === target.side;
                const onLine = target
                  ? comments?.filter(
                      (c) =>
                        c.file === f.path &&
                        c.line === target.line &&
                        c.side === target.side,
                    )
                  : undefined;
                return (
                  <Fragment key={i}>
                    <div
                      className={
                        (line.kind === "add"
                          ? "dl-add"
                          : line.kind === "del"
                            ? "dl-del"
                            : line.kind === "hunk"
                              ? "dl-hunk"
                              : line.kind === "meta"
                                ? "dl-meta"
                                : "") + (target ? " dl-commentable" : "")
                      }
                      onClick={() => {
                        if (!target) return;
                        setActive({ file: f.path, ...target });
                        setDraftText("");
                      }}
                    >
                      {line.text || " "}
                      {onLine && onLine.length > 0 && (
                        <span
                          className="dl-comment-badge"
                          title={`${onLine.length} comment(s) on this line`}
                        >
                          💬 {onLine.length}
                        </span>
                      )}
                    </div>
                    {isOpen && (
                      <div className="dl-composer">
                        <textarea
                          autoFocus
                          rows={2}
                          placeholder="Leave a comment on this line…"
                          value={draftText}
                          onChange={(e) => setDraftText(e.target.value)}
                          onKeyDown={(e) => {
                            if (
                              (e.metaKey || e.ctrlKey) &&
                              e.key === "Enter"
                            ) {
                              e.preventDefault();
                              submit();
                            }
                            if (e.key === "Escape") setActive(undefined);
                          }}
                        />
                        <div className="dl-composer-actions">
                          <button
                            type="button"
                            className="b"
                            onClick={() => setActive(undefined)}
                          >
                            Cancel
                          </button>
                          <button
                            type="button"
                            className="b ok"
                            disabled={!draftText.trim()}
                            onClick={submit}
                          >
                            Add comment
                          </button>
                        </div>
                      </div>
                    )}
                  </Fragment>
                );
              })}
            </div>
          </details>
        );
      })}
    </div>
  );
}
