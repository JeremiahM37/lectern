// Pure arithmetic for the phone gestures (pull to refresh, swipe a card),
// kept free of the DOM so the thresholds are tested as plain functions.

/** How far the pull indicator travels for a finger that moved dy pixels
 * down: it follows the finger at half speed and slows further past the
 * trigger, like a native list. */
export function pullOffset(dy: number, trigger = PULL_TRIGGER): number {
  if (dy <= 0) return 0;
  const half = dy / 2;
  return half <= trigger ? half : trigger + Math.sqrt(half - trigger) * 4;
}

export const PULL_TRIGGER = 64;

/** Which way a drag is going, once it has moved far enough to tell. A
 * vertical drag is a scroll and never becomes a swipe. */
export function dragAxis(dx: number, dy: number): "x" | "y" | undefined {
  const ax = Math.abs(dx),
    ay = Math.abs(dy);
  if (ax < 10 && ay < 10) return undefined;
  return ax > ay * 1.3 ? "x" : "y";
}

export type SwipeSide = "left" | "right";

/** A released swipe acts when it passed 30% of the card (at least 72 px),
 * or was flicked fast; otherwise it springs back. */
export function swipeCommits(dx: number, width: number, velocity: number): SwipeSide | undefined {
  const threshold = Math.max(72, width * 0.3);
  if (Math.abs(dx) >= threshold || (Math.abs(dx) > 36 && Math.abs(velocity) > 0.7))
    return dx > 0 ? "right" : "left";
  return undefined;
}

/** A card only moves toward a side that has an action; beyond the action's
 * reach it resists. */
export function swipeOffset(dx: number, left: boolean, right: boolean, width: number): number {
  if ((dx > 0 && !right) || (dx < 0 && !left)) return dx / 6;
  const reach = width * 0.45;
  const a = Math.abs(dx);
  return Math.sign(dx) * (a <= reach ? a : reach + (a - reach) / 4);
}
