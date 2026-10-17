package chat

import (
	"strings"
	"testing"
)

func TestForeignSessionHeaderOnlyForAnotherProject(t *testing.T) {
	cases := []struct {
		name, savedIn, here, want string
	}{
		{"same folder", "/home/u/proj", "/home/u/proj", ""},
		{"trailing slash is the same folder", "/home/u/proj/", "/home/u/proj", ""},
		{"another project", "/home/u/other", "/home/u/proj", "from another project: /home/u/other"},
		{"unknown origin gets no header", "", "/home/u/proj", ""},
		{"unknown current folder gets no header", "/home/u/other", "", ""},
	}
	for _, c := range cases {
		if got := ForeignSessionHeader(c.savedIn, c.here); got != c.want {
			t.Errorf("%s: ForeignSessionHeader(%q, %q) = %q, want %q", c.name, c.savedIn, c.here, got, c.want)
		}
	}
}

func TestForeignSessionHeaderShowsAboveTranscript(t *testing.T) {
	v, _ := newView(t, Options{})
	v.AddUser("picked up from the other repo", 0, t0, false)
	v.SetSessionHeader(ForeignSessionHeader("/home/u/other", "/home/u/proj"))
	first := strings.Split(plain(v), "\n")[0]
	if !strings.Contains(first, "── from another project: /home/u/other ──") {
		t.Fatalf("foreign header should open the transcript, got %q", first)
	}
	if v.SessionHeader() != "from another project: /home/u/other" {
		t.Fatalf("SessionHeader = %q", v.SessionHeader())
	}
}

// Restore starts from a clean header, so a foreign header set before it is
// lost. The resume path must therefore set the header after the restore.
func TestRestoreClearsForeignHeaderUnlessSetAfter(t *testing.T) {
	v, _ := newView(t, Options{})
	v.SetSessionHeader("from another project: /home/u/other")
	v.Restore([]Record{{Role: roleUser, Text: "hi"}})
	if v.SessionHeader() != "" {
		t.Fatalf("Restore should clear a header set before it, got %q", v.SessionHeader())
	}
	v.SetSessionHeader("from another project: /home/u/other")
	if v.SessionHeader() != "from another project: /home/u/other" {
		t.Fatalf("header set after Restore should stick, got %q", v.SessionHeader())
	}
}
