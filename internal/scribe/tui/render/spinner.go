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
)
