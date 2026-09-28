// What Enter does in the chat box (docs/design/simple-ui.md "Chat box"):
// Enter sends and Shift+Enter adds a line, like every chat app. On a
// touch-only device there is no Shift key, so there Enter adds a line and the
// Send button sends — unless the person chose otherwise in Settings → Basics.
// Ctrl/Cmd+Enter sends everywhere.
export type EnterMode = "auto" | "send" | "newline";
export const ENTER_PREF = "chat.enter";

export function touchOnly(): boolean {
  try {
    return matchMedia("(hover: none) and (pointer: coarse)").matches;
  } catch {
    return false;
  }
}

export function enterSends(mode: EnterMode, touch: boolean): boolean {
  return mode === "send" || (mode === "auto" && !touch);
}

export function shouldSend(
  key: { key: string; shiftKey: boolean; ctrlKey: boolean; metaKey: boolean; altKey: boolean; isComposing: boolean },
  mode: EnterMode,
  touch: boolean,
): boolean {
  if (key.key !== "Enter" || key.isComposing || key.altKey) return false;
  if (key.ctrlKey || key.metaKey) return true;
  return !key.shiftKey && enterSends(mode, touch);
}
