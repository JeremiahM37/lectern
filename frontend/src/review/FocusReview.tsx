import { useEffect, useRef, useState, type ReactNode } from "react";
import { t, useLocale } from "../i18n";

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
  const visible = scroller.clientHeight - inset;
  // Walk from the hunk last jumped to while it is still on screen: when the
  // view cannot scroll any further, positions alone would find the same
  // hunk again and navigation would stall on a short file.
  const last = Number(container.dataset.hunkAt ?? -1);
  let index: number;
  if (last >= 0 && last < hunks.length && offset(hunks[last]!) > -24 && offset(hunks[last]!) < visible) {
    index = last + dir;
  } else if (dir === 1) {
    index = hunks.findIndex((h) => offset(h) > 12);
  } else {
    index = hunks.length - 1 - [...hunks].reverse().findIndex((h) => offset(h) < -12);
    if (index >= hunks.length) index = -1;
  }
  const target = hunks[index];
  if (!target) return false;
  container.dataset.hunkAt = String(index);
  scroller.scrollTop += offset(target) - 4;
  for (const h of hunks) h.classList.remove("dl-hunk-current");
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
  useLocale();
  const body = useRef<HTMLDivElement>(null);
  const [pendingEdge, setPendingEdge] = useState<"first" | "last" | undefined>();
  const file = files[index];

  // After moving to another file by hunk navigation, land on its first (or
  // last) hunk once it has rendered.
  useEffect(() => {
    const el = body.current;
    if (!el) return;
    el.scrollTop = 0;
    delete el.dataset.hunkAt;
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
    <div className="focus-review" role="region" aria-label={t("review.focus.label")}>
      <header className="focus-bar">
        <button type="button" className="b" aria-label={t("review.focus.leave")} onClick={onClose}>
          ✕
        </button>
        <div className="focus-title">
          <span className="focus-count">
            {t("review.focus.count", { index: index + 1, total: files.length, viewed: viewedCount })}
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
          {file.viewed ? t("review.focus.viewed") : t("review.focus.markViewed")}
        </button>
      </header>
      <div className="focus-body" ref={body}>
        {renderFile(index)}
      </div>
      <footer className="focus-nav">
        <button type="button" className="b" disabled={index === 0} onClick={() => onIndex(index - 1)}>
          {t("review.focus.prevFile")}
        </button>
        <button type="button" className="b" onClick={() => hunk(-1)}>
          {t("review.focus.prevHunk")}
        </button>
        <button type="button" className="b" onClick={() => hunk(1)}>
          {t("review.focus.nextHunk")}
        </button>
        <button
          type="button"
          className="b"
          disabled={index >= files.length - 1}
          onClick={() => onIndex(index + 1)}
        >
          {t("review.focus.nextFile")}
        </button>
        {footer}
      </footer>
    </div>
  );
}
