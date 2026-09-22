// Android IMEs edit a textarea value, not a stream of terminal keystrokes.
// Let the browser finish each edit and translate it once. In particular a
// prediction replaces the composing word; xterm 5's 229-key and composition
// handlers otherwise both send that word (and never erase the old one).
export function replacementBytes(previous: string, next: string): string {
  const segmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" });
  const parts = (value: string) => Array.from(segmenter.segment(value), s => s.segment);
  const before = parts(previous), after = parts(next);
  let same = 0;
  while (same < before.length && same < after.length && before[same] === after[same]) same++;
  return "\x7f".repeat(before.length - same) + after.slice(same).join("");
}

export function installAndroidInput(host: HTMLElement, textarea: HTMLTextAreaElement,
  send: (text: string) => void) {
  const abort = new AbortController();
  textarea.setAttribute("inputmode", "text");
  let previous = "", composing = false, hardwareKey = false;
  const reset = () => { previous = ""; textarea.value = ""; composing = false; };
  const listen = <K extends keyof HTMLElementEventMap>(name: K, fn: (event: HTMLElementEventMap[K]) => void) =>
    host.addEventListener(name, event => {
      if (event.target === textarea) fn(event);
    }, { capture: true, signal: abort.signal });
  for (const name of ["keydown", "keyup", "keypress"] as const)
    listen(name, (event: KeyboardEvent) => {
      if (event.type === "keydown") hardwareKey = event.keyCode !== 229 && !event.isComposing;
      if (event.type === "keyup") hardwareKey = false;
      if (event.keyCode === 229 || event.isComposing) event.stopImmediatePropagation();
    });
  for (const name of ["compositionstart", "compositionupdate", "compositionend"] as const)
    listen(name, (event: CompositionEvent) => {
      composing = event.type !== "compositionend";
      event.stopImmediatePropagation();
    });
  listen("beforeinput", (event: InputEvent) => {
    if (hardwareKey) return; // a physical key is already handled by xterm
    if (event.inputType === "insertFromPaste") return; // xterm owns bracketed paste
    event.stopImmediatePropagation();
    // Gboard's non-composing prediction path moves the helper caret to zero
    // without deleting its old value, then inserts the replacement word.
    // Only our current word is retained here, and external cursor/selection
    // actions reset it. Translate this native replacement before it appends.
    if (event.inputType === "insertText" && event.data && event.data.length > 1 &&
        previous && textarea.value === previous && textarea.selectionStart === 0 &&
        textarea.selectionEnd === 0 && !composing) {
      event.preventDefault();
      const next = event.data;
      send(replacementBytes(previous, next));
      previous = next;
      textarea.value = next;
      textarea.setSelectionRange(next.length, next.length);
      if (/\s$/.test(next)) reset();
    }
    if (event.inputType === "deleteContentBackward" && !previous && !textarea.value) {
      event.preventDefault();
      send("\x7f");
    }
  });
  listen("input", (raw: Event) => {
    const event = raw as InputEvent;
    if (hardwareKey) return;
    if (event.inputType === "insertFromPaste") return;
    event.stopImmediatePropagation();
    const next = textarea.value;
    const bytes = replacementBytes(previous, next);
    previous = next;
    // Android's virtual-key events can leave the helper's selection at zero.
    // The mirrored context is always the text immediately before the cursor.
    textarea.setSelectionRange(next.length, next.length);
    if (bytes) send(bytes.replace(/\r?\n/g, "\r"));
    // Keep only the current word available to predictions. External cursor,
    // paste and control keys also reset this context via Engine.input().
    if (!composing && !event.isComposing && /\s$/.test(next)) reset();
  });
  listen("blur", reset);
  return { reset, dispose: () => abort.abort() };
}
