// Local echo for a slow link, in the spirit of mosh. On a phone with a poor
// connection every typed letter took a full round trip to appear, so typing
// into an agent felt broken. Letters typed at the cursor are drawn at once,
// underlined, over the terminal; the real echo replaces them as it lands.
//
// Predictions never enter the terminal's buffer: they are an overlay, so a
// wrong guess costs nothing but the guess disappearing. Shells, Claude Code
// and Codex all keep the real cursor right after the text being typed, so a
// guess is confirmed by the cursor moving right on the same row, and anything
// else (a new row, the cursor moving back) drops every guess.
//
// After a key whose effect is the program's to decide (Enter, an arrow, a
// rub-out of text already drawn) nothing is guessed until the cursor shows
// where it went: guessing from where it was would draw on the wrong row.
//
// A guess the program never echoes (a password prompt) times out, and then
// guessing stops until Enter, so the rest of a password is never drawn.

export interface Cursor {
  x: number;
  y: number;
}

/** What the overlay draws: `text` starting at cell (x, y). */
export interface Shown {
  x: number;
  y: number;
  text: string;
}

// Plain single-width letters only: wide characters, combining marks and
// anything a terminal might treat specially are left to the real echo.
const PREDICTABLE = /^[ -~ -ɏ]$/;

export class Predictor {
  private pending: { ch: string; at: number }[] = [];
  private anchor?: Cursor;
  private muted = false;
  // Waiting for the cursor to leave `from` (for Enter: to leave its row).
  private hold?: { from: Cursor; row: boolean; at: number };

  constructor(
    private now: () => number = () => Date.now(),
    /** How long a guess may wait for its echo before it counts as wrong. */
    readonly patience = 1500,
  ) {}

  /** The keys just sent, and where the cursor was when they were typed. */
  typed(text: string, cursor: Cursor, cols: number) {
    // An escape starts a key sequence (an arrow, a function key): its
    // printable tail is not text, and where the cursor goes is unknowable.
    if (text.includes("\x1b")) {
      this.wait(cursor, false);
      return;
    }
    for (const ch of text) {
      if (ch === "\r" || ch === "\n") {
        this.wait(cursor, true);
        this.muted = false;
        continue;
      }
      if (ch === "\x7f" || ch === "\b") {
        // Rubbing out a letter still on its way: the program gets both and
        // shows neither, so the overlay just drops it. Rubbing out one
        // already shown changes the screen in ways only the program knows.
        if (this.pending.length) this.pending.pop();
        else this.wait(cursor, false);
        continue;
      }
      if (!PREDICTABLE.test(ch)) {
        this.wait(cursor, false);
        continue;
      }
      if (this.hold && this.now() - this.hold.at >= this.patience) this.hold = undefined;
      if (this.muted || this.hold) {
        this.clear();
        continue;
      }
      if (!this.anchor) this.anchor = { ...cursor };
      // Never guess past the end of the row: wrapping is the program's call.
      if (this.anchor.x + this.pending.length + 1 >= cols) {
        this.clear();
        continue;
      }
      this.pending.push({ ch, at: this.now() });
    }
    if (!this.pending.length) this.anchor = undefined;
  }

  /** Output was drawn and the real cursor is now here. */
  drawn(cursor: Cursor) {
    const hold = this.hold;
    if (hold && (cursor.y !== hold.from.y || (!hold.row && cursor.x !== hold.from.x)))
      this.hold = undefined;
    if (!this.anchor || !this.pending.length) return;
    if (cursor.y !== this.anchor.y || cursor.x < this.anchor.x) {
      this.clear();
      return;
    }
    const echoed = Math.min(cursor.x - this.anchor.x, this.pending.length);
    if (echoed > 0) {
      this.pending.splice(0, echoed);
      this.anchor = { x: cursor.x, y: cursor.y };
    }
    if (!this.pending.length) this.anchor = undefined;
  }

  /** Drops guesses that waited too long, and stops guessing until Enter.
   * Returns whether anything was dropped. */
  expire(): boolean {
    const oldest = this.pending[0];
    if (!oldest || this.now() - oldest.at < this.patience) return false;
    this.clear();
    this.muted = true;
    return true;
  }

  clear() {
    this.pending = [];
    this.anchor = undefined;
  }

  private wait(cursor: Cursor, row: boolean) {
    this.clear();
    this.hold = { from: { ...cursor }, row, at: this.now() };
  }

  get shown(): Shown | undefined {
    if (!this.anchor || !this.pending.length) return undefined;
    return { ...this.anchor, text: this.pending.map((p) => p.ch).join("") };
  }

  /** When the oldest guess runs out of patience, for scheduling expire(). */
  get deadline(): number | undefined {
    const oldest = this.pending[0];
    return oldest ? oldest.at + this.patience : undefined;
  }
}
