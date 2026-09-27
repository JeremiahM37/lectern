import { useState } from "react";
import { Markdown } from "../sessions/markdown";
import { ago, labelStyle } from "./logic";
import type { Check, Item, Label, Reaction, TimelineEvent } from "./types";

// Small pieces the list, the pull request page and the issue page share.

export function StatePill({ item }: { item: Pick<Item, "kind" | "state" | "draft" | "source" | "status_type"> }) {
  let tone = "open",
    text = item.state;
  if (item.source === "linear" || item.source === "jira") {
    tone = { completed: "merged", done: "merged", canceled: "closed", started: "open", indeterminate: "open" }[item.status_type || ""] || "neutral";
  } else if (item.draft && item.state === "open") {
    tone = text = "draft";
  } else {
    tone = item.state;
  }
  return <span className={`th-state th-state-${tone}`}>{text}</span>;
}

const CHECK_ICON: Record<string, string> = { pass: "✓", fail: "✕", pending: "●", skipping: "–", cancel: "⊘", none: "" };

export function ChecksBadge({ checks }: { checks?: string }) {
  if (!checks || checks === "none") return null;
  const text = { pass: "checks pass", fail: "checks failing", pending: "checks running" }[checks] || checks;
  return (
    <span className={`th-badge th-checks-${checks}`} title={text}>
      <i aria-hidden="true">{CHECK_ICON[checks]}</i>
      <span className="th-badge-text">{text}</span>
    </span>
  );
}

export function CheckIcon({ check }: { check: Pick<Check, "status"> }) {
  return (
    <i className={`th-check-icon th-checks-${check.status}`} aria-label={check.status}>
      {CHECK_ICON[check.status]}
    </i>
  );
}

export function ReviewBadge({ review }: { review?: string }) {
  if (!review) return null;
  const text = { approved: "approved", changes_requested: "changes requested", review_required: "review required" }[review] || review;
  return <span className={`th-badge th-review-${review}`}>{text}</span>;
}

export function Labels({ labels, onRemove }: { labels: Label[]; onRemove?(name: string): void }) {
  if (!labels.length) return null;
  return (
    <span className="th-labels">
      {labels.map((l) => (
        <span key={l.name} className="th-label" style={labelStyle(l.color)}>
          {l.name}
          {onRemove && (
            <button type="button" className="th-x" aria-label={`Remove label ${l.name}`} onClick={() => onRemove(l.name)}>
              ×
            </button>
          )}
        </span>
      ))}
    </span>
  );
}

export function Reactions({ reactions }: { reactions?: Reaction[] }) {
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

export function Avatar({ name }: { name?: string }) {
  const initial = (name || "?").replace(/^@/, "").slice(0, 1).toUpperCase();
  let hue = 0;
  for (const c of name || "") hue = (hue * 31 + c.charCodeAt(0)) % 360;
  return (
    <span className="th-avatar" style={{ background: `hsl(${hue} 45% 32%)` }} aria-hidden="true">
      {initial}
    </span>
  );
}

const REVIEW_WORD: Record<string, string> = {
  approved: "approved these changes",
  changes_requested: "requested changes",
  commented: "reviewed",
  dismissed: "had a review dismissed",
};

/** The conversation: comments, reviews, commits and events in time order,
 * with a composer at the end when the caller can comment. */
export function Timeline({
  events,
  onComment,
  placeholder = "Leave a comment",
}: {
  events: TimelineEvent[];
  onComment?(body: string): Promise<void>;
  placeholder?: string;
}) {
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <div className="th-timeline">
      {events.length === 0 && <p className="th-muted">No conversation yet.</p>}
      {events.map((e, i) =>
        e.kind === "comment" ? (
          <article key={i} className="th-comment">
            <header>
              <Avatar name={e.author} />
              <b>{e.author || "someone"}</b>
              <time title={e.at}>{ago(e.at)}</time>
            </header>
            <div className="th-md">
              <Markdown text={e.body || ""} />
            </div>
            <Reactions reactions={e.reactions} />
          </article>
        ) : (
          <div key={i} className={`th-event th-event-${e.kind}${e.state ? " th-event-" + e.state : ""}`}>
            <span className="th-event-dot" aria-hidden="true" />
            {e.kind === "commit" ? (
              <span>
                <b>{e.author}</b> committed <code>{e.state}</code> {e.body}
              </span>
            ) : e.kind === "review" ? (
              <span>
                <b>{e.author}</b> {REVIEW_WORD[e.state || ""] || "reviewed"}
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
          <textarea aria-label="Comment" placeholder={placeholder} value={draft} onChange={(e) => setDraft(e.target.value)} rows={3} />
          <button className="b ok" type="submit" disabled={busy || !draft.trim()}>
            {busy ? "Posting…" : "Comment"}
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
      <input aria-label={label} list={id} autoFocus value={value} onChange={(e) => setValue(e.target.value)} placeholder="Type a name" />
      <datalist id={id}>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.hint}
          </option>
        ))}
      </datalist>
      <button type="submit" className="b ok" disabled={!value.trim()}>
        Add
      </button>
      <button type="button" className="b" onClick={() => setOpen(false)}>
        Cancel
      </button>
    </form>
  );
}
