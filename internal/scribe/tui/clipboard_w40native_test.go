package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Every test in this package gets a native clipboard write that does nothing,
// so a copy test never puts its text on the developer's clipboard. The tests
// that check the write itself replace it again.
func init() {
	nativeCopy = func(string) error { return nil }
}

// fakeEnv returns an environment lookup over a fixed map.
func fakeEnv(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// fakeHave reports true for the named commands only.
func fakeHave(names ...string) func(string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(c string) bool { return set[c] }
}

func TestNativeCopyToolPicksThePlatformTool(t *testing.T) {
	cases := []struct {
		name  string
		goos  string
		env   map[string]string
		have  []string
		want  string
		wantA []string
		ok    bool
	}{
		{"mac", "darwin", nil, []string{"pbcopy"}, "pbcopy", nil, true},
		{"mac without pbcopy", "darwin", nil, nil, "", nil, false},
		{"windows", "windows", nil, []string{"clip.exe"}, "clip.exe", nil, true},
		{"wayland prefers wl-copy", "linux",
			map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"},
			[]string{"wl-copy", "xclip"}, "wl-copy", nil, true},
		{"x11 uses xclip", "linux", map[string]string{"DISPLAY": ":0"},
			[]string{"xclip"}, "xclip", []string{"-selection", "clipboard"}, true},
		{"x11 falls back to xsel", "linux", map[string]string{"DISPLAY": ":0"},
			[]string{"xsel"}, "xsel", []string{"--clipboard", "--input"}, true},
		{"ssh with no display writes nothing", "linux", nil,
			[]string{"wl-copy", "xclip", "xsel"}, "", nil, false},
		{"unknown platform", "plan9", nil, []string{"pbcopy"}, "", nil, false},
	}
	for _, c := range cases {
		name, args, ok := nativeCopyTool(c.goos, fakeEnv(c.env), fakeHave(c.have...))
		if ok != c.ok || name != c.want {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, name, ok, c.want, c.ok)
			continue
		}
		if strings.Join(args, " ") != strings.Join(c.wantA, " ") {
			t.Errorf("%s: args %v, want %v", c.name, args, c.wantA)
		}
	}
}

// Ctrl+Y hands the copied text to the native write as well as to OSC 52.
func TestCtrlYAlsoWritesTheNativeClipboard(t *testing.T) {
	var got []string
	old := nativeCopy
	nativeCopy = func(s string) error { got = append(got, s); return nil }
	t.Cleanup(func() { nativeCopy = old })
	buf := swapClipboard(t)

	m, _ := newTestModel(t, 100, 30)
	m.cur().chat.AddUser("native copy text", 0, time.Now(), false)
	if _, handled := m.handleGlobalKey(tea.KeyMsg{Type: tea.KeyCtrlY}); !handled {
		t.Fatal("Ctrl+Y was not handled")
	}
	if len(got) != 1 || got[0] != "native copy text" {
		t.Fatalf("native write got %q, want the focused message once", got)
	}
	if !strings.Contains(buf.String(), "\x1b]52;c;") {
		t.Fatal("OSC 52 was dropped; the terminal copy must still go out")
	}
}

// A native write that fails is quiet: the copy still flashes as done.
func TestCtrlYNativeFailureIsQuiet(t *testing.T) {
	old := nativeCopy
	nativeCopy = func(string) error { return errNoNativeClipboard }
	t.Cleanup(func() { nativeCopy = old })
	swapClipboard(t)

	m, _ := newTestModel(t, 100, 30)
	m.cur().chat.AddUser("quiet", 0, time.Now(), false)
	m.handleGlobalKey(tea.KeyMsg{Type: tea.KeyCtrlY})
	if m.flashKind == "err" || !strings.HasPrefix(m.flash, "Copied the message (") {
		t.Fatalf("flash = %q (%s), want the ordinary copy line", m.flash, m.flashKind)
	}
}

// A failed attach is a failure the user must act on, so it also opens an
// alert (spec 01 §7.3), not only the flash line.
func TestAttachFailureOpensAlert(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.attachImageFile(m.cur(), "/nonexistent-w40/picture.png")
	if m.flashKind != "err" || !strings.HasPrefix(m.flash, "Can't attach /nonexistent-w40/picture.png") {
		t.Fatalf("flash = %q (%s), want the attach error", m.flash, m.flashKind)
	}
	if n := errorAlerts(m); n != 1 {
		t.Fatalf("error alerts = %d, want 1", n)
	}
}
