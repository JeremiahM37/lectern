package web

import (
	"net/url"
	"strings"
	"testing"
)

func md(t *testing.T, src string) string {
	t.Helper()
	base, _ := url.Parse("https://example.com/a/b")
	_, out := htmlToMarkdown(src, base)
	return out
}

func TestMarkdownBasics(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"heading":        {"<h3>Deep</h3><p>text</p>", "### Deep\n\ntext"},
		"bare text":      {"just words", "just words"},
		"relative link":  {`<p><a href="../c?x=1">go</a></p>`, "[go](https://example.com/c?x=1)"},
		"fragment link":  {`<p><a href="#top">top</a></p>`, "top"},
		"javascript url": {`<p><a href="javascript:void(0)">x</a></p>`, "x"},
		"pre no lang":    {"<pre>a\n  b</pre>", "```\na\n  b\n```"},
		"fence escape":   {"<pre>x ``` y</pre>", "````\nx ``` y\n````"},
		"entities":       {"<p>a&nbsp;&lt;b&gt; &copy;</p>", "a <b> ©"},
		"ordered list":   {"<ol><li>one</li><li>two</li></ol>", "1. one\n2. two"},
		"self-closing":   {"<p>a<br/>b</p>", "a b"},
		"unclosed tags":  {"<div><p>one<p>two</div>", "one\n\ntwo"},
		"comment hidden": {"<p>a<!-- hidden -->b</p>", "ab"},
		"image alt":      {`<p>x <img src="a.png" alt="chart"></p>`, "x [image: chart]"},
	}
	for name, c := range cases {
		if got := md(t, c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestMarkdownBlockquote(t *testing.T) {
	got := md(t, "<blockquote><p>quoted</p><p>more</p></blockquote>")
	if got != "> quoted\n>\n> more" {
		t.Fatalf("got %q", got)
	}
}

func TestParserSurvivesGarbage(t *testing.T) {
	// Nothing here may panic or loop forever.
	for _, src := range []string{"<", "<a href=", "<<<>>>", "</", "<div attr=\"unterminated", "<script>never closed", "<!--", "&#xZZ;"} {
		_ = md(t, src)
	}
}

func TestRawTextScriptIsNotParsed(t *testing.T) {
	doc := parseHTML("<script>if (a<b) document.write('<p>x</p>')</script><p>after</p>")
	got := md(t, "<script>if (a<b) document.write('<p>x</p>')</script><p>after</p>")
	if got != "after" {
		t.Fatalf("got %q", got)
	}
	var raw strings.Builder
	rawText(doc, &raw)
	if !strings.Contains(raw.String(), "if (a<b) document.write('<p>x</p>')") {
		t.Fatal("script body lost from the tree")
	}
}
