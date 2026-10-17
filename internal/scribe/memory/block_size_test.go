package memory

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// injectBlockBaselineBytes is the size of the fixture block before the
// per-entry signals and footer were added (bodies inlined, same fixture).
// The block is paid for on every turn, so growth is held to a small ceiling.
const injectBlockBaselineBytes = 1163

// injectSizeFixture saves five records that all match one query and one
// file, the shape a busy turn produces. Titles and bodies are typical
// lengths for records the memory tool writes.
func injectSizeFixture(t *testing.T) *Stores {
	t.Helper()
	s, _ := newStores(t)
	recs := []Record{
		{Category: CatBug, Title: "Gateway retry loop ignores Retry-After on 429 responses", Body: strings.Repeat("The retry helper backs off a fixed 200ms and never reads the header, so a rate-limited gateway gets hammered. ", 3), Files: []string{"internal/llm/retry.go"}, Pinned: true},
		{Category: CatDecision, Title: "Retry budget is per request, not per session", Body: "We chose a per-request budget of three attempts so one slow gateway cannot starve the whole turn. Revisit if streaming retries land.", Files: []string{"internal/llm/retry.go"}},
		{Category: CatConvention, Title: "Gateway errors are wrapped with the provider name", Body: "Wrap every gateway error with the provider so the TUI can say which backend failed.", Files: []string{"internal/llm/gateway.go"}},
		{Category: CatFact, Title: "Gateway timeout defaults to ninety seconds", Body: "The default request timeout for the gateway client is ninety seconds; long reasoning models need the override."},
		{Category: CatPreference, Title: "Prefer table tests for retry policy changes", Body: "Table-driven tests with named cases, one row per status code."},
	}
	for _, r := range recs {
		mustAdd(t, s.Project, r)
	}
	return s
}

// TestInjectBlockSizeGrowth holds the injected block to a small growth over
// the pre-change baseline. Signals add a few words per entry and the footer
// appears only when a body is cut.
func TestInjectBlockSizeGrowth(t *testing.T) {
	s := injectSizeFixture(t)
	block := s.Inject("gateway retry backoff", []string{"internal/llm/retry.go"})
	if block == "" {
		t.Fatal("expected a block")
	}
	t.Logf("injected block: %d bytes (baseline %d)", len(block), injectBlockBaselineBytes)
	ceiling := injectBlockBaselineBytes * 110 / 100
	if len(block) > ceiling {
		t.Fatalf("block grew to %d bytes, over the %d byte ceiling (110%% of baseline %d)", len(block), ceiling, injectBlockBaselineBytes)
	}
	if strings.Contains(block, detailsFooter) {
		t.Fatal("no body is cut in this fixture, so the footer must not appear")
	}
}

func TestInjectEntrySignals(t *testing.T) {
	s := injectSizeFixture(t)
	block := s.Inject("gateway retry backoff", []string{"internal/llm/retry.go"})
	lines := strings.Split(block, "\n")

	// The pinned record that also matched the file, the text and the
	// semantic embedding: every signal, in spec order, with the cosine.
	first := lines[1]
	if !strings.HasPrefix(first, "[bug] ") || !strings.Contains(first, " — Gateway retry loop ignores Retry-After") {
		t.Fatalf("first entry head: %q", first)
	}
	if !strings.HasSuffix(first, " · via file, sem 0.48, fts, ★") {
		t.Fatalf("first entry signals: %q", first)
	}
	// An entry with only a text match has just that signal.
	if !strings.Contains(block, "[fact] ") || !strings.Contains(block, "· via fts\n") {
		t.Fatalf("expected a text-only entry:\n%s", block)
	}
}

func TestInjectMarksCutBodiesAndFooter(t *testing.T) {
	s, _ := newStores(t)
	long := strings.Repeat("kestrel ", 120) // well over injectBodyChars
	mustAdd(t, s.Project, Record{Category: CatBug, Title: "Kestrel gateway cut", Body: long})
	mustAdd(t, s.Project, Record{Category: CatFact, Title: "Kestrel gateway short", Body: "Short kestrel note."})

	block := s.Inject("kestrel gateway", nil)
	var cutHead, shortHead string
	for _, l := range strings.Split(block, "\n") {
		switch {
		case strings.Contains(l, "Kestrel gateway cut"):
			cutHead = l
		case strings.Contains(l, "Kestrel gateway short"):
			shortHead = l
		}
	}
	if !strings.HasSuffix(cutHead, detailsMarker) {
		t.Fatalf("cut entry must carry the marker: %q", cutHead)
	}
	if strings.Contains(shortHead, "↳") {
		t.Fatalf("an entry shown in full must not be marked: %q", shortHead)
	}
	if !strings.Contains(block, "\n"+detailsFooter+"\n</recalled_memories>") {
		t.Fatalf("footer must sit just before the closing tag:\n%s", block)
	}
}

func TestSearchLabelsTrigramMatch(t *testing.T) {
	s, _ := newStores(t)
	mustAdd(t, s.Project, Record{Category: CatFact, Title: "Quokkalinter setup", Body: "Install the quokkalinter hook once per clone."})
	// "kalin" is inside one word, so only the trigram index can find it.
	got, err := s.Project.Search("kalin", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !contains(got[0].Why, "trigram") || contains(got[0].Why, "fts") {
		t.Fatalf("substring match should be labelled trigram only, got %+v", got)
	}
}

func TestSignalTextSemanticCosine(t *testing.T) {
	cases := []struct {
		sim  float64
		want string
	}{
		{0.35, "sem"},
		{0.40, "sem 0.40"},
		{0.48, "sem 0.48"},
	}
	for _, c := range cases {
		got := signalText(Scored{Why: []string{"similar"}, Sim: c.sim})
		if got != c.want {
			t.Errorf("sim %.2f: signalText = %q, want %q", c.sim, got, c.want)
		}
	}
	if got := signalText(Scored{Why: []string{"co-change", "pinned"}}); got != "co-change, ★" {
		t.Errorf("co-change and pinned: %q", got)
	}
	if got := signalText(Scored{}); got != "" {
		t.Errorf("no signals should render nothing, got %q", got)
	}
}

// TestSearchResultsCarryFooter covers the memory tool's own results: each
// line shows its signals, and the listing says how to read a body.
func TestSearchResultsCarryFooter(t *testing.T) {
	s, _ := newStores(t)
	mustAdd(t, s.Project, Record{Category: CatBug, Title: "Lantern cache stalls", Body: "Flush the lantern cache on reload."})
	mustAdd(t, s.Project, Record{Category: CatFact, Title: "Lantern colour", Body: "Amber, not gold."})
	reg := tools.NewRegistry()
	Register(reg, s)

	res := runTool(t, reg, `{"action":"search","query":"lantern cache"}`)
	if res.IsError {
		t.Fatalf("search: %+v", res)
	}
	if !strings.Contains(res.Output, "via fts") || !strings.Contains(res.Output, "↳ has details") {
		t.Fatalf("search lines need signals and the details marker:\n%s", res.Output)
	}
	if !strings.HasSuffix(res.Output, detailsFooter) {
		t.Fatalf("search results with a body need the footer:\n%s", res.Output)
	}
}

func TestListingFooterOnlyWhenABodyIsHidden(t *testing.T) {
	withBody := listing([]Scored{{Record: Record{ID: "aaaaaaaa11", Scope: "project", Category: "fact", Title: "Has body", Body: "x"}}})
	if !strings.HasSuffix(withBody, detailsFooter) {
		t.Fatalf("listing with a body must end with the footer:\n%s", withBody)
	}
	bare := listing([]Scored{{Record: Record{ID: "bbbbbbbb22", Scope: "project", Category: "fact", Title: "No body"}}})
	if strings.Contains(bare, "↳") || strings.Contains(bare, "get") {
		t.Fatalf("listing with no bodies must have no marker or footer:\n%s", bare)
	}
}
