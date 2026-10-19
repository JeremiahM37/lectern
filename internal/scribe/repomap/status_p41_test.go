package repomap

import (
	"context"
	"testing"
)

// A failed build keeps its error for Status, so the /repo-map view can show it
// even when the caller dropped the error. A later good build clears it.
func TestStatusKeepsTheLastBuildError(t *testing.T) {
	m, root := wave9Map(t)
	if st := m.Status(); st.BuildError != "" {
		t.Fatalf("a finished build reports an error: %q", st.BuildError)
	}
	// The home directory and the filesystem root are refused before any read.
	m.root = "/"
	if err := m.Build(context.Background(), nil); err == nil {
		t.Fatal("building the filesystem root must fail")
	}
	if st := m.Status(); st.BuildError == "" {
		t.Fatal("the failed build's error is missing from Status")
	}
	m.root = root
	if err := m.Build(context.Background(), nil); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if st := m.Status(); st.BuildError != "" {
		t.Fatalf("a good build left the error %q in Status", st.BuildError)
	}
}

// Status reports the size of the last render in estimated tokens; zero before
// any render.
func TestStatusReportsTheLastRenderSize(t *testing.T) {
	m, _ := wave9Map(t)
	if st := m.Status(); st.MapTokens != 0 {
		t.Fatalf("MapTokens before any render = %d", st.MapTokens)
	}
	text := m.Render(2500, nil)
	if text == "" {
		t.Fatal("the fixture rendered an empty map")
	}
	want := estTokens(text)
	if st := m.Status(); st.MapTokens != want {
		t.Fatalf("MapTokens = %d, want %d", st.MapTokens, want)
	}
}
