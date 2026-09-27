package filelinks

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type vector struct {
	Name    string   `json:"name"`
	Workdir string   `json:"workdir"`
	Rows    []string `json:"rows"`
	Wrapped []bool   `json:"wrapped"`
	Width   int      `json:"width"`
	Row     int      `json:"row"`
	Col     int      `json:"col"`
	Expect  *expect  `json:"expect"`
}

type expect struct {
	Kind     string   `json:"kind"`
	Path     string   `json:"path"`
	URL      string   `json:"url"`
	External bool     `json:"external"`
	Verify   bool     `json:"verify"`
	Line     int      `json:"line"`
	Column   int      `json:"column"`
	Spans    [][3]int `json:"spans"`
}

// The same vectors frontend/src/terminal/links.test.ts runs, so the native
// client and the web terminal agree on every case.
func TestSharedVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases      []vector `json:"cases"`
		Hyperlinks []struct {
			Name    string  `json:"name"`
			Workdir string  `json:"workdir"`
			URI     string  `json:"uri"`
			Expect  *expect `json:"expect"`
		} `json:"hyperlinks"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) < 10 {
		t.Fatalf("only %d vectors", len(file.Cases))
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			rows := make([]Row, len(c.Rows))
			for i, text := range c.Rows {
				rows[i] = Row{Text: text, Wrapped: i < len(c.Wrapped) && c.Wrapped[i]}
			}
			link, ok := LinkAt(rows, c.Row, c.Col, c.Workdir, c.Width)
			if c.Expect == nil {
				if ok {
					t.Fatalf("expected no link, got %+v", link)
				}
				return
			}
			if !ok {
				t.Fatal("expected a link, got none")
			}
			e := c.Expect
			got := [][3]int{}
			for _, s := range link.Spans {
				got = append(got, [3]int{s.Row, s.Start, s.End})
			}
			want := e.Spans
			if want == nil {
				want = got
			}
			if link.Kind != e.Kind || link.Path != e.Path || link.URL != e.URL || link.External != e.External ||
				link.Verify != e.Verify || link.Line != e.Line || link.Column != e.Column || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v spans %v\nwant %+v", link, got, *e)
			}
		})
	}
	if len(file.Hyperlinks) == 0 {
		t.Fatal("no hyperlink vectors")
	}
	for _, c := range file.Hyperlinks {
		t.Run("osc8 "+c.Name, func(t *testing.T) {
			link, ok := HyperlinkTarget(c.URI, c.Workdir)
			if c.Expect == nil {
				if ok {
					t.Fatalf("expected nothing, got %+v", link)
				}
				return
			}
			e := c.Expect
			if !ok || link.Kind != e.Kind || link.Path != e.Path || link.URL != e.URL || link.External != e.External || link.Verify != e.Verify {
				t.Fatalf("got %+v ok=%v, want %+v", link, ok, *e)
			}
		})
	}
}
