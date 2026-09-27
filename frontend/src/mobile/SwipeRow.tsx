// A list row a finger can swipe sideways to act on, like a mail app: right
// to approve what the session is waiting on, left to archive it. The row
// follows the finger, shows what letting go will do, and springs back when
// the swipe was short. Vertical drags still scroll the list (touch-action),
// and a mouse never swipes: the same actions stay on the card's buttons.
import { useRef, useState, type ReactNode } from "react";
import { dragAxis, swipeCommits, swipeOffset, type SwipeSide } from "./gestures";
import { haptic } from "./haptics";

export interface SwipeAction {
  label: string;
  tone: "ok" | "warn";
  run: () => void | Promise<unknown>;
}

export function SwipeRow({ left, right, children, id }: { left?: SwipeAction; right?: SwipeAction; children: ReactNode; id?: string }) {
  const row = useRef<HTMLDivElement>(null),
    card = useRef<HTMLDivElement>(null);
  const [side, setSide] = useState<SwipeSide>();
  const drag = useRef<{ id: number; x: number; y: number; t: number; axis?: "x" | "y"; dx: number; v: number; ready?: SwipeSide } | undefined>(undefined);
  if (!left && !right) return <>{children}</>;
  const move = (px: number, animate: boolean) => {
    const node = card.current;
    if (!node) return;
    node.style.transition = animate ? "transform .22s cubic-bezier(.2,.8,.2,1)" : "none";
    node.style.transform = px ? `translateX(${px}px)` : "";
  };
  return (
    <div
      ref={row}
      className={`swipe-row ${side ? "swiping-" + side : ""}`}
      data-swipe-row={id}
      onPointerDown={(e) => {
        if (e.pointerType !== "touch" || !e.isPrimary) return;
        if (e.target instanceof Element && e.target.closest("input,textarea,select,.action-menu[open]")) return;
        drag.current = { id: e.pointerId, x: e.clientX, y: e.clientY, t: performance.now(), dx: 0, v: 0 };
      }}
      onPointerMove={(e) => {
        const d = drag.current;
        if (!d || d.id !== e.pointerId) return;
        const dx = e.clientX - d.x,
          dy = e.clientY - d.y;
        d.axis ??= dragAxis(dx, dy);
        if (d.axis !== "x") {
          if (d.axis === "y") drag.current = undefined;
          return;
        }
        try {
          row.current?.setPointerCapture(e.pointerId);
        } catch {
          /* already released */
        }
        const now = performance.now();
        d.v = (dx - d.dx) / Math.max(1, now - d.t);
        d.dx = dx;
        d.t = now;
        const width = row.current?.clientWidth || 360;
        const next = dx > 0 ? (right ? "right" : undefined) : left ? "left" : undefined;
        setSide(next);
        const ready = swipeCommits(dx, width, 0);
        if (ready && ready === next && d.ready !== ready) haptic("tick");
        d.ready = ready === next ? ready : undefined;
        move(swipeOffset(dx, !!left, !!right, width), false);
      }}
      onPointerUp={(e) => {
        const d = drag.current;
        drag.current = undefined;
        if (!d || d.id !== e.pointerId || d.axis !== "x") return;
        const width = row.current?.clientWidth || 360;
        const done = swipeCommits(d.dx, width, d.v);
        const action = done === "right" ? right : done === "left" ? left : undefined;
        if (!action) {
          move(0, true);
          setSide(undefined);
          return;
        }
        haptic("confirm");
        move(Math.sign(d.dx) * width, true);
        void Promise.resolve(action.run()).finally(() => {
          move(0, true);
          setSide(undefined);
        });
      }}
      onPointerCancel={() => {
        drag.current = undefined;
        move(0, true);
        setSide(undefined);
      }}
      onClickCapture={(e) => {
        // The click that ends a swipe is not a tap on a button under it.
        if (side) {
          e.preventDefault();
          e.stopPropagation();
        }
      }}
    >
      {right && <div className={`swipe-under swipe-right ${right.tone}`} aria-hidden="true">{right.label}</div>}
      {left && <div className={`swipe-under swipe-left ${left.tone}`} aria-hidden="true">{left.label}</div>}
      <div ref={card} className="swipe-card">
        {children}
      </div>
    </div>
  );
}
