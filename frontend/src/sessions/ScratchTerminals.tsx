import type { ReactNode } from "react";
import type { SessionView } from "../types";
import { SessionGroups, type GroupMode } from "./SessionGroups";

// Blank shells nobody has made a project of yet. They are real tracked
// sessions with real terminals, so they stay on this dashboard — but they are
// not AI conversations, and mixing them into that list buries both. The
// section is always rendered and always visible, so its heading tells a person
// where quick shells go even when there are none.
export function ScratchTerminals({
  items,
  mode,
  query,
  collapsed,
  onToggle,
  render,
}: {
  items: SessionView[];
  mode: GroupMode;
  query: string;
  collapsed: Set<string>;
  onToggle: (key: string, open: boolean) => void;
  render: (session: SessionView) => ReactNode;
}) {
  // Collapsing a group in one list must not collapse the same label in the
  // other; the shared preference is still one stored set, namespaced here.
  const prefix = "scratch:";
  const ownKeys = new Set(
    [...collapsed]
      .filter((key) => key.startsWith(prefix))
      .map((key) => key.slice(prefix.length)),
  );
  return (
    <section
      className="session-section scratch-terminals"
      id="scratch-terminals"
      aria-labelledby="scratch-terminals-title"
    >
      <h3 className="session-section-title" id="scratch-terminals-title">
        Scratch terminals
      </h3>
      <p className="session-section-sub">
        Blank shells with no project. Attach to use one, or make it a project to
        keep its files and terminal with your work.
      </p>
      <div className="session-grid">
        {items.length ? (
          <SessionGroups
            items={items}
            mode={mode}
            query={query}
            collapsed={ownKeys}
            onToggle={(key, open) => onToggle(prefix + key, open)}
            render={render}
          />
        ) : (
          <div className="hint">
            {query
              ? "No scratch terminals match your search."
              : "No scratch terminals. Open a blank shell from the Terminals tab and it waits here, out of the way of your sessions."}
          </div>
        )}
      </div>
    </section>
  );
}
