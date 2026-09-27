// A small bump under the thumb when a gesture commits (a swipe that will
// act, a pull that will refresh, a long press that selected text). The
// Android app does it natively through the view, which needs no permission;
// a browser that supports the Vibration API gets a short pulse; anything
// else does nothing.
import { nativeBridge } from "../native/bridge";

export type Haptic = "tick" | "confirm" | "warn";

const patterns: Record<Haptic, number | number[]> = { tick: 8, confirm: [10, 40, 14], warn: [30, 60, 30] };

export function haptic(kind: Haptic = "tick") {
  const bridge = nativeBridge();
  if (bridge?.haptic) {
    try {
      bridge.haptic(kind);
      return;
    } catch {
      /* fall through to the browser */
    }
  }
  try {
    navigator.vibrate?.(patterns[kind]);
  } catch {
    /* not allowed without a user gesture, or unsupported */
  }
}
