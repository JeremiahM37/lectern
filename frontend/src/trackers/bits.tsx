import { useState } from "react";
import { t, useLocale } from "../i18n";
import { Markdown } from "../sessions/markdown";
import { ago, EMOJIS, hasStatuses, labelStyle } from "./logic";
import { RichEditor } from "./RichEditor";
import type { Check, Item, Label, Reaction, TimelineEvent } from "./types";

// Small pieces the list, the pull request page and the issue page share.

// Forge states are fixed words; Linear and Jira state names come from the
// tracker and are shown as they are.
const FORGE_STATE = (): Record<string, string> => ({
  open: t("trackers.state.open"),
  closed: t("trackers.state.closed"),
  merged: t("trackers.state.merged"),
  draft: t("trackers.state.draft"),
});

export function StatePill({ item }: { item: Pick<Item, "kind" | "state" | "draft" | "source" | "status_type"> }) {
  useLocale();
  let tone = "open",
    text = item.state;
  if (hasStatuses(item)) {
    tone = { completed: "merged", done: "merged", canceled: "closed", started: "open", indeterminate: "open" }[item.status_type || ""] || "neutral";
  } else if (item.draft && item.state === "open") {
    tone = text = "draft";
  } else {
    tone = item.state;
  }
  if (item.source !== "linear" && item.source !== "jira") text = FORGE_STATE()[text] || text;
  return <span className={`th-state th-state-${tone}`}>{text}</span>;
}

const CHECK_ICON: Record<string, string> = { pass: "✓", fail: "✕", pending: "●", skipping: "–", cancel: "⊘", none: "" };

export function ChecksBadge({ checks }: { checks?: string }) {
  useLocale();
  if (!checks || checks === "none") return null;
  const text =
    { pass: t("trackers.checks.pass"), fail: t("trackers.checks.fail"), pending: t("trackers.checks.pending") }[checks] || checks;
  return (
    <span className={`th-badge th-checks-${checks}`} title={text}>
      <i aria-hidden="true">{CHECK_ICON[checks]}</i>
      <span className="th-badge-text">{text}</span>
    </span>
  );
}

const CHECK_STATUS = (): Record<string, string> => ({
  pass: t("trackers.checkStatus.pass"),
  fail: t("trackers.checkStatus.fail"),
  pending: t("trackers.checkStatus.pending"),
  skipping: t("trackers.checkStatus.skipping"),
  cancel: t("trackers.checkStatus.cancel"),
  none: t("trackers.checkStatus.none"),
});

export function CheckIcon({ check }: { check: Pick<Check, "status"> }) {
  useLocale();
  return (
    <i className={`th-check-icon th-checks-${check.status}`} aria-label={CHECK_STATUS()[check.status] || check.status}>
      {CHECK_ICON[check.status]}
    </i>
  );
}

export function ReviewBadge({ review }: { review?: string }) {
  useLocale();
  if (!review) return null;
  const text =
    {
      approved: t("trackers.review.approved"),
      changes_requested: t("trackers.review.changesRequested"),
      review_required: t("trackers.review.reviewRequired"),
    }[review] || review;
  return <span className={`th-badge th-review-${review}`}>{text}</span>;
}

export function Labels({ labels, onRemove }: { labels: Label[]; onRemove?(name: string): void }) {
  useLocale();
  if (!labels.length) return null;
  return (
    <span className="th-labels">
      {labels.map((l) => (
        <span key={l.name} className="th-label" style={labelStyle(l.color, document.documentElement.dataset.theme === "light" ? "light" : "dark")}>
          {l.name}
          {onRemove && (
            <button type="button" className="th-x" aria-label={t("trackers.labels.remove", { name: l.name })} onClick={() => onRemove(l.name)}>
              ×
            </button>
          )}
        </span>
      ))}
    </span>
  );
}

export function Reactions({ reactions }: { reactions?: Reaction[] }) {
  useLocale();
  if (!reactions?.length) return null;
  return (
    <span className="th-reactions">
      {reactions.map((r) => (
        <span key={r.emoji} className="th-reaction">
          {r.emoji} {r.count}
        </span>
      ))}
    </span>
  );
}

/** Reaction counts plus an add-reaction button, where the host takes them. */
export function ReactionBar({ reactions, onReact, label }: { reactions?: Reaction[]; onReact?(emoji: string): Promise<void>; label: string }) {
  useLocale();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  if (!onReact) return <Reactions reactions={reactions} />;
  return (
    <span className="th-reactions">
      {(reactions || []).map((r) => {
        const name = EMOJIS.find(([, g]) => g === r.emoji)?.[0];
        return (
          <button key={r.emoji} type="button" className="th-reaction" disabled={busy || !name} aria-label={t("trackers.reactions.reactCount", { emoji: r.emoji, n: r.count })}
            onClick={() => { if (!name) return; setBusy(true); void onReact(name).finally(() => setBusy(false)); }}>
            {r.emoji} {r.count}
          </button>
        );
      })}
      <span className="th-react-add">
        <button type="button" className="th-reaction th-react-open" aria-label={t("trackers.reactions.add", { label })} aria-expanded={open} onClick={() => setOpen(!open)}>
          ☺+
        </button>
        {open && (
          <span className="th-react-menu" role="menu">
            {EMOJIS.map(([name, glyph]) => (
              <button key={name} type="button" role="menuitem" aria-label={t("trackers.reactions.react", { emoji: name })} disabled={busy}
                onClick={() => { setOpen(false); setBusy(true); void onReact(name).finally(() => setBusy(false)); }}>
                {glyph}
              </button>
            ))}
          </span>
        )}
      </span>
    </span>
  );
}

export function Avatar({ name }: { name?: string }) {
  useLocale();
  const initial = (name || "?").replace(/^@/, "").slice(0, 1).toUpperCase();
  let hue = 0;
  for (const c of name || "") hue = (hue * 31 + c.charCodeAt(0)) % 360;
  return (
    <span className="th-avatar" style={{ background: `hsl(${hue} 45% 32%)` }} aria-hidden="true">
      {initial}
    </span>
  );
}

// Follows a reviewer's name: "sam approved these changes".
const REVIEW_WORD = (): Record<string, string> => ({
  approved: t("trackers.timeline.review.approved"),
  changes_requested: t("trackers.timeline.review.changesRequested"),
  commented: t("trackers.timeline.review.commented"),
  dismissed: t("trackers.timeline.review.dismissed"),
});

/** The conversation: comments, reviews, commits and events in time order,
 * with a composer at the end when the caller can comment. */
export function Timeline({
  events,
  onComment,
  onReact,
  placeholder,
}: {
  events: TimelineEvent[];
  onComment?(body: string): Promise<void>;
  /** react to a comment by its id; absent where the host has no reactions */
  onReact?(subject: string, emoji: string): Promise<void>;
  placeholder?: string;
}) {
  useLocale();
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <div className="th-timeline">
      {events.length === 0 && <p className="th-muted">{t("trackers.timeline.empty")}</p>}
      {events.map((e, i) =>
        e.kind === "comment" ? (
          <article key={i} className="th-comment">
            <header>
              <Avatar name={e.author} />
              <b>{e.author || t("trackers.timeline.someone")}</b>
              <time title={e.at}>{ago(e.at)}</time>
            </header>
            <div className="th-md">
              <Markdown text={e.body || ""} />
            </div>
            <ReactionBar reactions={e.reactions} label={e.author ? t("trackers.reactions.commentBy", { name: e.author }) : t("trackers.reactions.thisComment")}
              onReact={onReact && e.id ? (emoji) => onReact(e.id!, emoji) : undefined} />
          </article>
        ) : (
          <div key={i} className={`th-event th-event-${e.kind}${e.state ? " th-event-" + e.state : ""}`}>
            <span className="th-event-dot" aria-hidden="true" />
            {e.kind === "commit" ? (
              <span>
                <b>{e.author}</b> {t("trackers.timeline.committed")} <code>{e.state}</code> {e.body}
              </span>
            ) : e.kind === "review" ? (
              <span>
                <b>{e.author}</b> {REVIEW_WORD()[e.state || ""] || t("trackers.timeline.review.commented")}
                {e.body ? <>: {e.body}</> : null}
              </span>
            ) : (
              <span>
                {e.author ? <b>{e.author} </b> : null}
                {e.body}
              </span>
            )}
            <time title={e.at}>{ago(e.at)}</time>
          </div>
        ),
      )}
      {onComment && (
        <form
          className="th-composer"
          onSubmit={(ev) => {
            ev.preventDefault();
            if (!draft.trim() || busy) return;
            setBusy(true);
            void onComment(draft.trim())
              .then(() => setDraft(""))
              .finally(() => setBusy(false));
          }}
        >
          <RichEditor label={t("trackers.timeline.commentField")} placeholder={placeholder ?? t("trackers.timeline.placeholder")} value={draft} onChange={setDraft} rows={3} />
          <button className="b ok" type="submit" disabled={busy || !draft.trim()}>
            {busy ? t("trackers.timeline.posting") : t("trackers.timeline.comment")}
          </button>
        </form>
      )}
    </div>
  );
}

/** A picker that offers known names but accepts any typed one. */
export function NamePicker({
  label,
  options,
  onPick,
  busy,
}: {
  label: string;
  options: { value: string; hint?: string }[];
  onPick(value: string): void;
  busy?: boolean;
}) {
  useLocale();
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState("");
  const id = `th-pick-${label.replace(/\W+/g, "-").toLowerCase()}`;
  if (!open)
    return (
      <button type="button" className="th-add" onClick={() => setOpen(true)} disabled={busy}>
        + {label}
      </button>
    );
  return (
    <form
      className="th-picker"
      onSubmit={(e) => {
        e.preventDefault();
        if (!value.trim()) return;
        onPick(value.trim());
        setValue("");
        setOpen(false);
      }}
    >
      <input aria-label={label} list={id} autoFocus value={value} onChange={(e) => setValue(e.target.value)} placeholder={t("trackers.picker.placeholder")} />
      <datalist id={id}>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.hint}
          </option>
        ))}
      </datalist>
      <button type="submit" className="b ok" disabled={!value.trim()}>
        {t("trackers.picker.add")}
      </button>
      <button type="button" className="b" onClick={() => setOpen(false)}>
        {t("trackers.cancel")}
      </button>
    </form>
  );
}
