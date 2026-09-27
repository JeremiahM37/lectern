// Small stroke icons for the file tools, drawn on the same 24px grid as
// shell/Icon.tsx.
const PATHS = {
  "file-plus": "M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8zM14 3v5h5M12 11v6M9 14h6",
  "folder-plus": "M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2zM12 10v6M9 13h6",
  refresh: "M20 11a8 8 0 1 0-2.3 5.7M20 5v6h-6",
  collapse: "M4 6h16M4 12h10M4 18h6M17 15l3 3-3 3",
  download: "M12 4v11M7 10l5 5 5-5M5 20h14",
  search: "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM21 21l-5-5",
  files: "M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8zM14 3v5h5",
  goto: "M4 6h9M4 12h6M4 18h9M15 9l6 6M21 9v6h-6",
  expand: "M4 9V4h5M20 15v5h-5M4 4l6 6M20 20l-6-6",
  close: "M6 6l12 12M18 6L6 18",
} as const;

export type IconName = keyof typeof PATHS;

export function FileIcon({ name, size = 16 }: { name: IconName; size?: number }) {
  return (
    <svg viewBox="0 0 24 24" width={size} height={size} fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={PATHS[name]} />
    </svg>
  );
}
