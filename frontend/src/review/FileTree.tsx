import { useMemo, useState } from "react";
import { buildTree, type TreeNode } from "./diffModel";

export interface TreeFile {
  path: string;
  additions?: number;
  deletions?: number;
  viewed?: boolean;
  comments?: number;
}

/**
 * The changed-file tree beside the diff: folders collapse, a filter narrows
 * it, and each file shows its +/− counts, open comments and whether it has
 * been marked viewed. Selecting a file scrolls the diff to it.
 */
export function FileTree({
  files,
  selected,
  onSelect,
}: {
  files: TreeFile[];
  selected?: string;
  onSelect(path: string): void;
}) {
  const [query, setQuery] = useState("");
  const [closed, setClosed] = useState<Record<string, boolean>>({});
  const byPath = useMemo(() => new Map(files.map((f) => [f.path, f])), [files]);
  const shown = files.filter((f) => f.path.toLowerCase().includes(query.toLowerCase()));
  const shownKey = shown.map((f) => f.path).join("\n");
  const tree = useMemo(() => buildTree(shownKey ? shownKey.split("\n") : []), [shownKey]);
  const viewedCount = files.filter((f) => f.viewed).length;

  function node(n: TreeNode, depth: number) {
    if (!n.file) {
      const isClosed = closed[n.path];
      return (
        <li key={`d:${n.path}`}>
          <button
            type="button"
            className="ftree-dir"
            style={{ paddingLeft: 8 + depth * 12 }}
            aria-expanded={!isClosed}
            onClick={() => setClosed((c) => ({ ...c, [n.path]: !isClosed }))}
          >
            <span aria-hidden="true">{isClosed ? "▸" : "▾"}</span> {n.name}
          </button>
          {!isClosed && <ul>{n.children.map((c) => node(c, depth + 1))}</ul>}
        </li>
      );
    }
    const f = byPath.get(n.path);
    return (
      <li key={`f:${n.path}`}>
        <button
          type="button"
          className={"ftree-file" + (f?.viewed ? " viewed" : "")}
          style={{ paddingLeft: 8 + depth * 12 }}
          aria-current={selected === n.path ? "true" : undefined}
          title={n.path}
          onClick={() => onSelect(n.path)}
        >
          <span className="ftree-name">{n.name}</span>
          {f?.comments ? <span className="ftree-comments">💬{f.comments}</span> : null}
          <span className="ftree-pm">
            <b className="a">+{f?.additions ?? 0}</b> <b className="d">−{f?.deletions ?? 0}</b>
          </span>
          {f?.viewed && (
            <span className="ftree-check" aria-label="viewed">
              ✓
            </span>
          )}
        </button>
      </li>
    );
  }

  return (
    <nav className="ftree" aria-label="Changed files">
      <div className="ftree-head">
        <input
          type="search"
          placeholder="Filter files"
          aria-label="Filter changed files"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <span className="sub">
          {viewedCount}/{files.length} viewed
        </span>
      </div>
      <ul>{tree.map((n) => node(n, 0))}</ul>
      {!shown.length && <p className="sub">No matching files.</p>}
    </nav>
  );
}
