package repomap

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// registerQueryTool adds soul_query, the pipeline tool over the code map
// (spec 04 2.7). It is called from Register, so it is available wherever the
// other code map tools are.
func registerQueryTool(r *tools.Registry, m *Map) {
	r.Register(&queryTool{m: m, spec: llm.ToolSpec{
		Name: "soul_query",
		Description: "Answer a structural question about the repository by chaining stages over the code map. Each stage narrows a set of files, " +
			"and the set holds the most important files first (at most 200). Stages: find (symbol or file name), search (literal text, case-insensitive), " +
			"filter (ext, pathContains, recent), deps (direction imports or imported_by, replaces the set with the files it imports or that import it), " +
			"limit (keep the n most important). End with outline (symbols of each file, no file reads) or read (file lines, optionally by ranges). " +
			"Examples: which files import X: [find X, deps imported_by]. Where is Y defined and used: [find Y, outline] and [search Y, outline]. " +
			"Most important files under a directory: [filter pathContains dir, limit 10]. What changed recently: [filter recent]. " +
			"Output lists the stages with the file count after each one, then the answer.",
		Schema: tools.Schema(map[string]any{
			"pipeline": tools.Arr("Stages, run in order. At most 12. outline or read must be last. deps cannot be first.", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"op":           tools.Enum("The stage", queryOps...),
					"pattern":      tools.Str("search: literal text to find in file contents (case-insensitive)"),
					"query":        tools.Str("find: symbol name, fragment, or file name"),
					"ext":          tools.Str("filter: file extension, such as .go"),
					"pathContains": tools.Str("filter: text the repo-relative path must contain (case-insensitive)"),
					"recent":       tools.Bool("filter: only files modified in the last 48 hours"),
					"direction":    tools.Enum("deps: imports (what the files import) or imported_by (what imports them)", "imports", "imported_by"),
					"n":            tools.Int("limit: how many of the most important files to keep (1 to 200)"),
					"ranges": tools.Arr("read: line ranges to show, each {start, end}, 1-based and inclusive", map[string]any{
						"type": "object",
						"properties": map[string]any{
							"start": tools.Int("First line"),
							"end":   tools.Int("Last line"),
						},
					}),
				},
				"required": []string{"op"},
			}),
		}, "pipeline"),
	}})
}

// queryTool runs soul_query. It only reads the map and the files it names, so
// it is read-only.
type queryTool struct {
	m    *Map
	spec llm.ToolSpec
}

func (t *queryTool) Spec() llm.ToolSpec { return t.spec }

// ReadOnly is always true: a pipeline never writes.
func (t *queryTool) ReadOnly() bool { return true }

func (t *queryTool) Run(ctx context.Context, _ *tools.Env, args json.RawMessage) (res tools.Result) {
	defer func() {
		if r := recover(); r != nil {
			res = tools.Errorf("soul_query failed: %v. Try a simpler pipeline, or use grep if this persists.", r)
		}
	}()
	if t.m == nil {
		return tools.Errorf("the code map is not available in this session. Use grep and glob instead.")
	}
	return runQueryTool(ctx, t.m, args)
}

// runQueryTool decodes, validates and runs one pipeline and formats the
// answer. Validation runs first, so a malformed pipeline never touches the map.
func runQueryTool(ctx context.Context, m *Map, args json.RawMessage) tools.Result {
	var a struct {
		Pipeline []queryStage `json:"pipeline"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	if err := validatePipeline(a.Pipeline); err != nil {
		return tools.Errorf("%v", err)
	}
	res, err := runQuery(ctx, m, a.Pipeline)
	if err != nil {
		return tools.Errorf("soul_query stopped: %v", err)
	}
	return tools.Result{
		Output:  formatQuery(res),
		Summary: fmt.Sprintf("%d files", len(res.Files)),
	}
}
