package render

// Spinner is a sequence of animation frames.
type Spinner []string

// Frame returns the frame for tick. The tick is the animation counter (one step
// per interval); negative ticks wrap too.
func Frame(set Spinner, tick int) string {
	if len(set) == 0 {
		return ""
	}
	i := tick % len(set)
	if i < 0 {
		i += len(set)
	}
	return set[i]
}

// Spinner frame sets from the behavioural spec (section 10).
var (
	// Braille is the generic spinner (150 ms per frame).
	Braille = Spinner{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	// Explore is the phase spinner for code exploration.
	Explore = Spinner{"◴", "◷", "◶", "◵"}
	// Edit is the phase spinner for edits: a bar that fills and empties.
	Edit = Spinner{"▏", "▎", "▍", "▌", "▋", "▊", "▉", "█", "▉", "▊", "▋", "▌", "▍", "▎"}
	// Dispatch is the phase spinner for sub-agent dispatch.
	Dispatch = Spinner{"◇", "◈", "◆", "◈"}
	// Dots is the trailing ellipsis animation.
	Dots = Spinner{".", "..", "...", "..", "."}
	// LoadingDots is the six-step ellipsis used on the busy line.
	LoadingDots = Spinner{".", "..", "...", "..", ".", ".."}
	// Compact is the context-compaction spinner.
	Compact = Spinner{"◐", "◓", "◑", "◒"}
	// Rune is the busy-line rune spinner (15 frames, 2.25 s cycle at 150 ms).
	Rune = Spinner{"ᛁ", "ᚲ", "ᚠ", "ᛊ", "ᛏ", "ᛉ", "ᛞ", "ᛞ", "ᛉ", "ᛏ", "ᛊ", "ᚠ", "ᚲ", "ᛁ", "ᛁ"}
	// EditorOpen is the editor pane's opening wipe (6 frames, 60 ms each).
	EditorOpen = Spinner{"  ░", " ░▒", "░▒▓", "▒▓█", "▓██", "███"}
)

// MarqueeHold is how many ticks a marquee rests at each end before it moves.
const MarqueeHold = 10

// Marquee returns the width-cell window of a long plain-text label for tick.
// It holds at the start, scrolls one cell per tick to the end, holds there and
// starts again. Text that fits is returned unchanged. Cells are counted as
// runes, which is right for labels (paths, names, phrases) and not for
// wide-glyph text. Use it only for the active label; inactive ones truncate.
func Marquee(s string, width, tick int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	over := len(r) - width
	if over <= 0 {
		return s
	}
	period := over + 2*MarqueeHold
	t := tick % period
	if t < 0 {
		t += period
	}
	off := over
	switch {
	case t < MarqueeHold:
		off = 0
	case t < MarqueeHold+over:
		off = t - MarqueeHold + 1
	}
	return string(r[off : off+width])
}
