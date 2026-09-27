import type { ReactNode } from "react";
import type { SessionView } from "../types";
import { t, useLocale } from "../i18n";
export type GroupMode = "none" | "group" | "project" | "target";
interface Node {
  label: string;
  children: Map<string, Node>;
  items: SessionView[];
  all: SessionView[];
}
export function SessionGroups({
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
  useLocale();
  if (mode === "none") return <>{items.map(render)}</>;
  const tree: Node = { label: "", children: new Map(), items: [], all: [] };
  for (const session of items) {
    const labels =
      mode === "group"
        ? session.group_path
          ? session.group_path.split("/")
          : ["\0"]
        : [
            session[mode === "project" ? "project_name" : "target_name"] ||
              "Unassigned",
          ];
    let node = tree;
    node.all.push(session);
    for (const label of labels) {
      if (!node.children.has(label))
        node.children.set(label, {
          // "Unassigned" stays the grouping key (it is in data-group-path and
          // the saved collapse state); only the heading is translated.
          label:
            label === "\0"
              ? t("sessions.groups.ungrouped")
              : mode !== "group" && label === "Unassigned"
                ? t("sessions.groups.unassigned")
                : label,
          children: new Map(),
          items: [],
          all: [],
        });
      node = node.children.get(label)!;
      node.all.push(session);
    }
    node.items.push(session);
  }
  function draw(node: Node, path: string[] = []): ReactNode {
    return (
      <>
        {node.items.map(render)}
        {[...node.children]
          .sort(([, a], [, b]) => a.label.localeCompare(b.label))
          .map(([label, child]) => {
            const next = [...path, label],
              key = JSON.stringify([mode, ...next]),
              open = !!query || !collapsed.has(key),
              waiting = child.all.filter(
                (row) => row.status === "waiting" && !row.ended_at,
              ).length;
            return (
              <details
                key={key}
                className="session-group"
                open={open}
                data-group-path={next.join("/").replaceAll("\0", "")}
              >
                <summary
                  onClick={(event) => {
                    event.preventDefault();
                    onToggle(key, !open);
                  }}
                >
                  <span>{child.label}</span>
                  <small>
                    {child.all.length}
                    {waiting ? t("sessions.groups.waiting", { n: waiting }) : ""}
                  </small>
                </summary>
                <div className="session-group-body">{draw(child, next)}</div>
              </details>
            );
          })}
      </>
    );
  }
  return draw(tree);
}
