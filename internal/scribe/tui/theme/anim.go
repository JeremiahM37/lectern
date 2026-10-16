package theme

import (
	"fmt"
	"math"
	"strconv"

	"github.com/charmbracelet/lipgloss"
)

// Lerp blends from a to b by t, where 0 is a and 1 is b (t is clamped). Only
// #rgb and #rrggbb colours blend. For anything else (no colour, an ANSI index)
// it returns a below the halfway point and b from it, so a palette without
// truecolor still changes at the midpoint instead of going blank.
func Lerp(a, b lipgloss.TerminalColor, t float64) lipgloss.TerminalColor {
	t = math.Max(0, math.Min(1, t))
	ar, ag, ab, okA := hexRGB(a)
	br, bg, bb, okB := hexRGB(b)
	if !okA || !okB {
		if t < 0.5 {
			return a
		}
		return b
	}
	mix := func(x, y int) int { return int(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", mix(ar, br), mix(ag, bg), mix(ab, bb)))
}

// Breath colours status text by a sine wave: tick runs through period steps
// per cycle (spec 10: lerp of textMuted towards brand, 20 ticks at 150 ms).
func Breath(a, b lipgloss.TerminalColor, tick, period int) lipgloss.TerminalColor {
	if period <= 0 {
		return a
	}
	phase := 2 * math.Pi * float64(mod(tick, period)) / float64(period)
	return Lerp(a, b, (math.Sin(phase)+1)/2)
}

// Ping is a triangle wave: a at tick 0, b halfway through period, a again at
// the end. The animated chat border uses it (spec 10: 6 steps, out and back).
func Ping(a, b lipgloss.TerminalColor, tick, period int) lipgloss.TerminalColor {
	if period <= 0 {
		return a
	}
	pos := float64(mod(tick, period)) / float64(period) // 0..1
	return Lerp(a, b, 1-math.Abs(2*pos-1))
}

func mod(x, m int) int {
	x %= m
	if x < 0 {
		x += m
	}
	return x
}

// hexRGB reads a #rgb or #rrggbb colour. ok is false for anything else.
func hexRGB(c lipgloss.TerminalColor) (r, g, b int, ok bool) {
	lc, isColor := c.(lipgloss.Color)
	if !isColor {
		return 0, 0, 0, false
	}
	s := string(lc)
	if len(s) == 4 && s[0] == '#' {
		s = "#" + string([]byte{s[1], s[1], s[2], s[2], s[3], s[3]})
	}
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff), true
}
