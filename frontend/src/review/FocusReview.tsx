import { useEffect, useRef, useState, type ReactNode } from "react";

export interface FocusFile {
  key: string;
  label: string;
  viewed: boolean;
}

/** Scrolls to the next (dir 1) or previous (-1) hunk header in container,
 * relative to the current reading position. scroller is the element that
 * actually scrolls (the container itself by default) and inset the height
 * of anything pinned over its top. Returns false at either end. */
export function jumpHunk(container: HTMLElement, dir: 1 | -1, scroller: HTMLElement = container, inset = 0): boolean {
  const hunks = Array.from(container.querySelectorAll<HTMLElement>(".dl-hunk"));
  const top = scroller.getBoundingClientRect().top + inset;
  const offset = (el: HTMLElement) => el.getBoundingClientRect().top - top;
  const target =
    dir === 1 ? hunks.find((h) => offset(h) > 12) : [...hunks].reverse().find((h) => offset(h) < -12);
  if (!target) return false;
  scroller.scrollTop += offset(target) - 4;
  target.classList.add("dl-hunk-current");
  window.setTimeout(() => target.classList.remove("dl-hunk-current"), 900);
  return true;
}

/**
 * Full-screen review, one file at a time — the phone's diff reviewer, and
 * available on desktop too. Previous/next hunk walks the file and then moves
 * on to the next one; "Mark viewed" records the file as reviewed and goes to
 * the next unviewed file. Comments are made by tapping a line, as anywhere
 * else in the review.
 */
export function FocusReview({
  files,
  index,
  onIndex,
  onToggleViewed,
  onClose,
  renderFile,
  footer,
}: {
  files: FocusFile[];
  index: number;
  onIndex(i: number): void;
  onToggleViewed(i: number, viewed: boolean): void;
  onClose(): void;
  renderFile(i: number): ReactNode;
  footer?: ReactNode;
}) {
  const body = useRef<HTMLDivElement>(null);
  const [pendingEdge, setPendingEdge] = useState<"first" | "last" | undefined>();
  const file = files[index];

  // After moving to another file by hunk navigation, land on its first (or
  // last) hunk once it has rendered.
  useEffect(() => {
    const el = body.current;
    if (!el) return;
    el.scrollTop = 0;
    if (!pendingEdge) return;
    const id = window.requestAnimationFrame(() => {
      if (pendingEdge === "last") {
        el.scrollTop = el.scrollHeight;
        jumpHunk(el, -1);
      } else {
        jumpHunk(el, 1);
      }
      setPendingEdge(undefined);
    });
    return () => window.cancelAnimationFrame(id);
  }, [index]);

  function hunk(dir: 1 | -1) {
    const el = body.current;
    if (el && jumpHunk(el, dir)) return;
    const next = index + dir;
    if (next < 0 || next >= files.length) return;
    setPendingEdge(dir === 1 ? "first" : "last");
    onIndex(next);
  }

  function markViewed() {
    if (!file) return;
    onToggleViewed(index, !file.viewed);
    if (!file.viewed) {
      const after = files.findIndex((f, i) => i > index && !f.viewed);
      const before = files.findIndex((f, i) => i < index && !f.viewed);
      const next = after >= 0 ? after : before;
      if (next >= 0) onIndex(next);
    }
  }

  useEffect(() => {
    function key(e: KeyboardEvent) {
      if (e.target instanceof HTMLElement && e.target.closest("textarea,input,select")) return;
      if (e.key === "j" || e.key === "]") hunk(1);
      else if (e.key === "k" || e.key === "[") hunk(-1);
      else if (e.key === "v") markViewed();
      else return;
      e.preventDefault();
    }
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  });

  if (!file) return null;
  const viewedCount = files.filter((f) => f.viewed).length;
  return (
    <div className="focus-review" role="region" aria-label="Review one file at a time">
      <header className="focus-bar">
        <button type="button" className="b" aria-label="Leave file-by-file review" onClick={onClose}>
          ✕
        </button>
        <div className="focus-title">
          <span className="focus-count">
            File {index + 1} of {files.length} · {viewedCount} viewed
          </span>
          <span className="focus-path" title={file.label}>
            {file.label}
          </span>
        </div>
        <button
          type="button"
          className={"b" + (file.viewed ? " ok" : "")}
          aria-pressed={file.viewed}
          onClick={markViewed}
        >
          {file.viewed ? "✓ Viewed" : "Mark viewed"}
        </button>
      </header>
      <div className="focus-body" ref={body}>
        {renderFile(index)}
      </div>
      <footer className="focus-nav">
        <button type="button" className="b" disabled={index === 0} onClick={() => onIndex(index - 1)}>
          ‹ File
        </button>
        <button type="button" className="b" onClick={() => hunk(-1)}>
          ▲ Prev hunk
        </button>
        <button type="button" className="b" onClick={() => hunk(1)}>
          Next hunk ▼
        </button>
        <button
          type="button"
          className="b"
          disabled={index >= files.length - 1}
          onClick={() => onIndex(index + 1)}
        >
          File ›
        </button>
        {footer}
      </footer>
    </div>
  );
}
