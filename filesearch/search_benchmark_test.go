package filesearch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkRankFileCandidates10K(b *testing.B) {
	benchmarkRankFileCandidates(b, 10_000, "f123go")
}

func BenchmarkRankFileCandidates100K(b *testing.B) {
	benchmarkRankFileCandidates(b, 100_000, "f123go")
}

func BenchmarkRankFileCandidates500K(b *testing.B) {
	benchmarkRankFileCandidates(b, 500_000, "f123go")
}

func BenchmarkRankFileCandidates100KEmpty(b *testing.B) {
	benchmarkRankFileCandidates(b, 100_000, "")
}

func benchmarkRankFileCandidates(b *testing.B, count int, query string) {
	b.Helper()
	root := filepath.Join(string(filepath.Separator), "workspace")
	candidates := make([]fileCandidate, count)
	for index := range candidates {
		name := fmt.Sprintf("file-%06d.go", index)
		candidates[index] = fileCandidate{
			path: filepath.Join(root, fmt.Sprintf("package-%03d", index%1_000), name),
			name: name,
		}
	}
	spec := fileSearchSpec{directory: root, query: query, recursive: true}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		matches := rankFileCandidates(context.Background(), root, spec, candidates)
		if len(matches) == 0 {
			b.Fatal("no matches")
		}
	}
}

func BenchmarkDiscoverFiles10K(b *testing.B) {
	root := b.TempDir()
	for directory := range 100 {
		path := filepath.Join(root, fmt.Sprintf("directory-%03d", directory))
		if err := os.Mkdir(path, 0o755); err != nil {
			b.Fatal(err)
		}
		for file := range 100 {
			name := filepath.Join(path, fmt.Sprintf("file-%03d.go", file))
			if err := os.WriteFile(name, nil, 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	spec := fileSearchSpec{directory: root, recursive: true}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		count := 0
		limited, err := discoverFiles(context.Background(), spec, func(batch []fileCandidate) error {
			count += len(batch)
			return nil
		})
		if err != nil || limited || count != 10_100 {
			b.Fatalf("count=%d limited=%t error=%v", count, limited, err)
		}
	}
}
