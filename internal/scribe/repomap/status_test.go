package repomap

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func statusRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	src := "package pkg\n\n// Add adds two numbers.\nfunc Add(a, b int) int { return a + b }\n"
	if err := os.WriteFile(filepath.Join(root, "pkg.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// The status view reports the cache size, the model summaries that still
// match their file, the mode and whether a summarizer is installed.
func TestStatusReportsSummariesAndCache(t *testing.T) {
	m := buildMap(t, statusRepo(t), Options{})
	st := m.Status()
	if st.DBBytes <= 0 {
		t.Errorf("DBBytes = %d, want the cache file size", st.DBBytes)
	}
	if st.SummaryMode != SummaryAST || st.HasSummarizer || st.AutoRegen || st.Summaries != 0 {
		t.Errorf("fresh status = %+v", st)
	}

	m.SetSummarizer(&fakeSummarizer{})
	m.SetAutoRegen(true)
	if !m.SetSummaryMode(SummaryLLM) {
		t.Fatal("llm mode refused")
	}
	if _, err := m.SummarizeMissing(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	st = m.Status()
	if st.Summaries == 0 || !st.HasSummarizer || !st.AutoRegen || st.SummaryMode != SummaryLLM {
		t.Errorf("after summarizing: %+v", st)
	}
}

// A summary is counted only while its file keeps the hash it was written
// for; an edit makes it stale and it drops out of the count.
func TestStatusDropsStaleSummaries(t *testing.T) {
	root := statusRepo(t)
	m := buildMap(t, root, Options{})
	m.SetSummarizer(&fakeSummarizer{})
	if _, err := m.SummarizeMissing(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	before := m.Status().Summaries
	if before == 0 {
		t.Fatal("no summaries to test staleness with")
	}
	if err := os.WriteFile(filepath.Join(root, "pkg.go"), []byte("package pkg\n\n// Sub subtracts.\nfunc Sub(a, b int) int { return a - b }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Update([]string{"pkg.go"}); err != nil {
		t.Fatal(err)
	}
	if got := m.Status().Summaries; got != 0 {
		t.Errorf("summaries after the file changed = %d, want 0", got)
	}
}

// An in-memory map has no cache file, so it reports no size.
func TestStatusInMemoryHasNoCacheSize(t *testing.T) {
	m := buildMap(t, statusRepo(t), Options{InMemory: true})
	if got := m.Status().DBBytes; got != 0 {
		t.Errorf("DBBytes = %d for an in-memory map, want 0", got)
	}
}
