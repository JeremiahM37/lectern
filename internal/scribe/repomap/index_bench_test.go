package repomap

import (
	"context"
	"os"
	"testing"
)

// BenchmarkBuildRepo indexes the repository named by REPOMAP_BENCH_ROOT from
// scratch (in memory) on every iteration. It is skipped unless the variable
// is set, so the normal suite never touches a large tree.
func BenchmarkBuildRepo(b *testing.B) {
	root := os.Getenv("REPOMAP_BENCH_ROOT")
	if root == "" {
		b.Skip("set REPOMAP_BENCH_ROOT to a repository to time")
	}
	for i := 0; i < b.N; i++ {
		m, err := Open(root, Options{InMemory: true})
		if err != nil {
			b.Fatal(err)
		}
		if err := m.Build(context.Background(), nil); err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(m.LastParsed()), "parsed")
		m.Close()
	}
}

// BenchmarkRebuildRepo measures a warm rebuild against an on-disk cache: the
// cost a session pays on every start once the cache exists.
func BenchmarkRebuildRepo(b *testing.B) {
	root := os.Getenv("REPOMAP_BENCH_ROOT")
	if root == "" {
		b.Skip("set REPOMAP_BENCH_ROOT to a repository to time")
	}
	m, err := Open(root, Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer m.Close()
	if err := m.Build(context.Background(), nil); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := m.Build(context.Background(), nil); err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(m.LastParsed()), "parsed")
	}
}
