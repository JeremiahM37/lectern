// Command sample prints Markdown, highlighted code and a diff in the default
// Scribe theme, so the rendering can be eyeballed with `go run`.
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

const doc = "# Scribe rendering\n\n" +
	"Plain paragraphs wrap to the width, and **strong**, *emphasis*, ~~strike~~ and `inline code` all keep their styling. " +
	"A link looks like [the spec](https://example.com/spec) and a bare URL like https://example.com/x is linkified.\n\n" +
	"## Lists\n\n" +
	"- first item with a longer sentence that has to wrap onto a second line to show the hanging indent\n" +
	"  - nested item\n" +
	"    - deeper item\n" +
	"- second item\n\n" +
	"1. step one\n2. step two\n3. step three\n\n" +
	"- [ ] write the renderer\n- [x] write the tests\n\n" +
	"> A block quote with a rail.\n> It can run over several lines and still wraps.\n\n" +
	"| Name | Kind | Notes |\n|:-----|:----:|------:|\n| read | tool | reads a file and returns its lines with numbers |\n| edit_file | tool | replaces text |\n\n" +
	"---\n\n" +
	"<system-reminder>hidden from the user</system-reminder>\n\n" +
	"```go\nfunc add(a, b int) int { return a + b } // sum\n```\n"

const goCode = `package main

// Greeter says hello.
func Greet(name string) string {
	msg := "hello, " + name
	if len(name) > 10 {
		return fmt.Sprintf("%s!", msg)
	}
	return msg
}
`

const pyCode = `class Cache:
    """Simple cache."""
    def get(self, key: str, default=None):
        # look it up
        return self.items.get(key, default)  # 42
`

const tsCode = `export async function load(id: number): Promise<User | null> {
  const res = await fetch("/api/users/" + id);
  return res.ok ? (await res.json()) as User : null;
}
`

const rustCode = `fn main() {
    let v: Vec<u32> = (1..=5).collect();
    println!("sum = {}", v.iter().sum::<u32>());
}
`

const sqlCode = `SELECT id, name FROM users
WHERE active = 1 -- only active
ORDER BY name LIMIT 10;
`

const diffText = `--- a/internal/server/handler.go
+++ b/internal/server/handler.go
@@ -10,14 +10,15 @@ func handle(w http.ResponseWriter, r *http.Request) {
 	ctx := r.Context()
 	user := lookup(ctx, r)
 	// unchanged one
 	// unchanged two
 	// unchanged three
 	// unchanged four
 	// unchanged five
-	timeout := 5 * time.Second
+	timeout := 30 * time.Second
 	if user == nil {
 		http.Error(w, "unauthorised", http.StatusUnauthorized)
 		return
 	}
 	// unchanged six
 	// unchanged seven
`

func main() {
	width := 80
	if v := os.Getenv("COLUMNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 20 {
			width = n
		}
	}
	th := theme.Default()
	icons.SetNerd(false)

	section("Markdown")
	fmt.Println(render.Markdown(doc, width, th, false))

	section("Code highlighting")
	for _, c := range []struct{ lang, code string }{
		{"go", goCode}, {"python", pyCode}, {"typescript", tsCode}, {"rust", rustCode}, {"sql", sqlCode},
	} {
		fmt.Printf("[%s]\n%s\n", c.lang, render.Highlight(c.code, c.lang, th))
	}

	section("Diff")
	fmt.Println(render.Diff(diffText, width, th, render.DiffOpts{LineNumbers: true}))
	fmt.Println()
	fmt.Println(render.Diff(diffText, width, th, render.DiffOpts{LineNumbers: true, SideBySide: true}))
}

func section(name string) {
	fmt.Printf("\n==== %s ====\n\n", name)
}
