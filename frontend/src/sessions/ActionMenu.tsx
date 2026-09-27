import { useEffect, useRef, type ReactNode } from "react";
export function ActionMenu({
  name,
  children,
}: {
  name: string;
  children: ReactNode;
}) {
  const root = useRef<HTMLDetailsElement>(null),
    panel = useRef<HTMLDivElement>(null);
  function position() {
    const menu = root.current,
      content = panel.current;
    if (!menu?.open || !content) return;
    menu.classList.remove("menu-above");
    content.style.transform = "";
    content.style.maxHeight = "";
    // A phone gets a bottom sheet instead (mobile/phone.css).
    const sheet = innerWidth <= 600 && matchMedia("(pointer: coarse), (hover: none)").matches;
    menu.classList.toggle("as-sheet", sheet);
    if (sheet) return;
    let left = 8,
      right = innerWidth - 8,
      top = 8,
      bottom = innerHeight - 8;
    const nav = document.querySelector("#tabbar");
    if (nav && getComputedStyle(nav).display !== "none") {
      const b = nav.getBoundingClientRect();
      if (b.width < innerWidth / 2 && b.height > innerHeight / 2)
        left = Math.max(left, b.right + 8);
      else if (b.width > innerWidth / 2 && b.top > innerHeight / 2)
        bottom = Math.min(bottom, b.top - 8);
    }
    const header = document.querySelector("#topbar");
    if (header && getComputedStyle(header).display !== "none")
      top = Math.max(top, header.getBoundingClientRect().bottom + 8);
    const box = menu.getBoundingClientRect(),
      below = bottom - box.bottom - 6,
      above = box.top - top - 6,
      upward = below < Math.min(content.scrollHeight, 420) && above > below;
    menu.classList.toggle("menu-above", upward);
    content.style.maxHeight =
      Math.max(0, Math.min(420, upward ? above : below)) + "px";
    const bounds = content.getBoundingClientRect(),
      dx =
        bounds.left < left
          ? left - bounds.left
          : bounds.right > right
            ? right - bounds.right
            : 0;
    content.style.transform = dx ? `translateX(${dx}px)` : "";
  }
  useEffect(() => {
    const close = (event: PointerEvent) => {
      if (
        event.target instanceof Node &&
        root.current &&
        // The sheet's dimmed backdrop is the menu's own ::before.
        (!root.current.contains(event.target) || event.target === root.current)
      )
        root.current.open = false;
    };
    const key = (event: KeyboardEvent) => {
      if (event.key === "Escape" && root.current?.open) {
        event.stopPropagation();
        root.current.open = false;
        root.current.querySelector("summary")?.focus();
      }
    };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", key);
    window.addEventListener("resize", position);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("keydown", key);
      window.removeEventListener("resize", position);
    };
  }, []);
  return (
    <details ref={root} className="action-menu" onToggle={position}>
      <summary aria-label={`More actions for ${name}`}>More ···</summary>
      <div
        ref={panel}
        className="action-menu-panel"
        onClick={(event) => {
          if (
            event.target instanceof Element &&
            event.target.closest("button,a") &&
            root.current
          )
            root.current.open = false;
        }}
      >
        {children}
      </div>
    </details>
  );
}
