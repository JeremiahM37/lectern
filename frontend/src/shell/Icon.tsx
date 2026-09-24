export function Icon({
  name,
  size = 20,
}: {
  name:
    | "board"
    | "sessions"
    | "terminals"
    | "deck"
    | "media"
    | "approvals"
    | "targets"
    | "brand"
    | "search"
    | "plus"
    | "evals";
  size?: number;
}) {
  return (
    <svg
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill="none"
      stroke="currentColor"
      strokeWidth="1.9"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {name === "board" ? (
        <>
          <rect x="3" y="4" width="5" height="16" rx="1.5" />
          <rect x="10" y="4" width="5" height="10" rx="1.5" />
          <rect x="17" y="4" width="4" height="14" rx="1.5" />
        </>
      ) : name === "sessions" || name === "terminals" ? (
        <>
          <rect x="3" y="4" width="18" height="16" rx="2" />
          <path d="M7 9l3 3-3 3M13 15h4" />
        </>
      ) : name === "media" ? (
        <>
          <rect x="3" y="5" width="18" height="14" rx="2" />
          <path d="M10 9.5v5l4.5-2.5z" />
        </>
      ) : name === "deck" ? (
        <>
          <rect x="3" y="4" width="8" height="7" rx="1.5" />
          <rect x="13" y="4" width="8" height="7" rx="1.5" />
          <rect x="3" y="13" width="8" height="7" rx="1.5" />
          <rect x="13" y="13" width="8" height="7" rx="1.5" />
        </>
      ) : name === "approvals" ? (
        <>
          <path d="M12 3l7 3v6c0 4.2-2.9 7.6-7 9-4.1-1.4-7-4.8-7-9V6z" />
          <path d="M9 12l2 2 4-4" />
        </>
      ) : name === "targets" ? (
        <>
          <rect x="3" y="4" width="18" height="6" rx="2" />
          <rect x="3" y="14" width="18" height="6" rx="2" />
          <path d="M7 7h.01M7 17h.01" />
        </>
      ) : name === "brand" ? (
        <>
          <rect x="3" y="4" width="18" height="6" rx="2" />
          <rect x="3" y="14" width="10" height="6" rx="2" />
          <path d="M17 14v6M20 17h-6" />
        </>
      ) : name === "search" ? (
        <>
          <circle cx="10" cy="10" r="6" />
          <path d="m15 15 5 5" />
        </>
      ) : name === "evals" ? (
        <>
          <rect x="3" y="3" width="8" height="8" rx="1.5" />
          <rect x="13" y="3" width="8" height="8" rx="1.5" />
          <rect x="3" y="13" width="8" height="8" rx="1.5" />
          <path d="M15.5 15.5l2 2 3-3" />
        </>
      ) : (
        <path d="M12 5v14M5 12h14" />
      )}
    </svg>
  );
}
