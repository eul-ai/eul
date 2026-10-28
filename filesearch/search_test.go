package filesearch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestResolveFileSearchSpecScopes(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "Code", "eul")
	writeSearchFile(t, filepath.Join(cwd, "terminal", "tui.go"), "package terminal")
	canonicalCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		query     string
		directory string
		leaf      string
		prefix    string
		recursive bool
		explicit  bool
	}{
		{name: "cwd", query: "terminal/tui", directory: cwd, leaf: "terminal/tui", recursive: true},
		{name: "cwd explicit", query: "./terminal/tui", directory: filepath.Join(cwd, "terminal"), leaf: "tui", prefix: "./terminal/", recursive: true, explicit: true},
		{name: "parent", query: "../eu", directory: filepath.Dir(cwd), leaf: "eu", prefix: "../", explicit: true},
		{name: "home", query: "~", directory: home, prefix: "~/", explicit: true},
		{name: "home child", query: "~/Code/", directory: filepath.Join(home, "Code"), prefix: "~/Code/", explicit: true},
		{name: "absolute", query: filepath.ToSlash(filepath.Dir(cwd)) + "/eu", directory: filepath.Dir(cwd), leaf: "eu", prefix: filepath.ToSlash(filepath.Dir(cwd)) + "/", explicit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec, err := resolveFileSearchSpec(cwd, canonicalCWD, home, test.query)
			if err != nil {
				t.Fatal(err)
			}
			wantDirectory, err := filepath.EvalSymlinks(test.directory)
			if err != nil {
				t.Fatal(err)
			}
			if spec.directory != wantDirectory || spec.query != test.leaf || spec.prefix != test.prefix || spec.recursive != test.recursive || spec.explicit != test.explicit {
				t.Fatalf("spec = %+v, want directory=%q query=%q prefix=%q recursive=%t explicit=%t", spec, wantDirectory, test.leaf, test.prefix, test.recursive, test.explicit)
			}
		})
	}

	hidden, err := resolveFileSearchSpec(cwd, canonicalCWD, home, "~/.")
	if err != nil {
		t.Fatal(err)
	}
	if !hidden.includeHidden || hidden.query != "." {
		t.Fatalf("hidden spec = %+v", hidden)
	}

	visibleExternal, err := resolveFileSearchSpec(cwd, canonicalCWD, home, "~/Code/e")
	if err != nil {
		t.Fatal(err)
	}
	hiddenExternal, err := resolveFileSearchSpec(cwd, canonicalCWD, home, "~/Code/.e")
	if err != nil {
		t.Fatal(err)
	}
	if visibleExternal.key() != hiddenExternal.key() {
		t.Fatalf("shallow hidden query changed catalog: visible=%+v hidden=%+v", visibleExternal.key(), hiddenExternal.key())
	}
	rescoredExternal, ok := rescoreFileSearchSpec(visibleExternal, "~/Code/eu")
	if !ok || rescoredExternal.directory != visibleExternal.directory || rescoredExternal.query != "eu" {
		t.Fatalf("rescored external spec = %+v, %t", rescoredExternal, ok)
	}
	if _, ok := rescoreFileSearchSpec(visibleExternal, "~/other/eu"); ok {
		t.Fatal("changed external directory reused its resolved scope")
	}

	hiddenGitHub, err := resolveFileSearchSpec(cwd, canonicalCWD, home, ".github/work")
	if err != nil {
		t.Fatal(err)
	}
	hiddenGitHubEdit, err := resolveFileSearchSpec(cwd, canonicalCWD, home, ".github/workflow")
	if err != nil {
		t.Fatal(err)
	}
	hiddenConfig, err := resolveFileSearchSpec(cwd, canonicalCWD, home, ".config/file")
	if err != nil {
		t.Fatal(err)
	}
	if hiddenGitHub.key() != hiddenGitHubEdit.key() || hiddenGitHub.key() == hiddenConfig.key() {
		t.Fatalf("hidden traversal keys = github=%+v edit=%+v config=%+v", hiddenGitHub.key(), hiddenGitHubEdit.key(), hiddenConfig.key())
	}
}

func TestResolveFileSearchSpecKeepsBrowseRootsShallow(t *testing.T) {
	home := t.TempDir()
	tests := []struct {
		name  string
		cwd   string
		query string
	}{
		{name: "bare home", cwd: home, query: "~"},
		{name: "home child filter", cwd: home, query: "~/child"},
		{name: "bare root", cwd: string(filepath.Separator), query: "/"},
		{name: "root child filter", cwd: string(filepath.Separator), query: "/var"},
		{name: "bare parent at root", cwd: string(filepath.Separator), query: "../"},
		{name: "parent child filter at root", cwd: string(filepath.Separator), query: "../var"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			canonicalCWD, err := filepath.EvalSymlinks(test.cwd)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := resolveFileSearchSpec(test.cwd, canonicalCWD, home, test.query)
			if err != nil {
				t.Fatal(err)
			}
			if spec.recursive {
				t.Fatalf("spec for %q from %q is recursive: %+v", test.query, test.cwd, spec)
			}
		})
	}
}

func TestDiscoverFilesAppliesDepthHiddenAndSymlinkPolicy(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeSearchFile(t, filepath.Join(root, "top.txt"), "top")
	writeSearchFile(t, filepath.Join(root, "nested", "deep.txt"), "deep")
	writeSearchFile(t, filepath.Join(root, ".github", "workflow.yml"), "workflow")
	writeSearchFile(t, filepath.Join(root, ".git", "config"), "git")
	writeSearchFile(t, filepath.Join(outside, "outside.txt"), "outside")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}

	shallow := collectDiscoveredPaths(t, fileSearchSpec{directory: root})
	if want := []string{".github/", "nested/", "top.txt"}; !slices.Equal(shallow, want) {
		t.Fatalf("shallow paths = %q, want %q", shallow, want)
	}

	recursive := collectDiscoveredPaths(t, fileSearchSpec{directory: root, recursive: true})
	if want := []string{"nested/", "nested/deep.txt", "top.txt"}; !slices.Equal(recursive, want) {
		t.Fatalf("recursive paths = %q, want %q", recursive, want)
	}

	hidden := collectDiscoveredPaths(t, fileSearchSpec{directory: root, recursive: true, includeHidden: true})
	if want := []string{".github/", "nested/", "nested/deep.txt", "top.txt"}; !slices.Equal(hidden, want) {
		t.Fatalf("hidden paths = %q, want %q", hidden, want)
	}

	namedHidden := collectDiscoveredPaths(t, fileSearchSpec{directory: root, query: ".github/work", recursive: true, includeHidden: true})
	if want := []string{".github/", ".github/workflow.yml", "nested/", "nested/deep.txt", "top.txt"}; !slices.Equal(namedHidden, want) {
		t.Fatalf("named hidden paths = %q, want %q", namedHidden, want)
	}

	gitRoot := collectDiscoveredPaths(t, fileSearchSpec{directory: filepath.Join(root, ".git"), recursive: true, includeHidden: true})
	if len(gitRoot) != 0 {
		t.Fatalf(".git root paths = %q", gitRoot)
	}
}

func TestDiscoverFilesHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := discoverFiles(ctx, fileSearchSpec{directory: t.TempDir(), recursive: true}, func([]fileCandidate) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestRankFileCandidatesUsesRelevanceBeforeLimit(t *testing.T) {
	cwd := t.TempDir()
	candidates := make([]fileCandidate, 0, 122)
	for index := range 120 {
		name := fmt.Sprintf("target-copy-%03d.go", index)
		candidates = append(candidates, fileCandidate{path: filepath.Join(cwd, "generated", name), name: name})
	}
	candidates = append(
		candidates,
		fileCandidate{path: filepath.Join(cwd, "target.go"), name: "target.go"},
		fileCandidate{path: filepath.Join(cwd, "target.go.dir"), name: "target.go", directory: true},
	)

	matches := rankFileCandidates(context.Background(), cwd, fileSearchSpec{directory: cwd, query: "target.go", recursive: true}, candidates)
	if len(matches) != maxResults {
		t.Fatalf("matches = %d, want %d", len(matches), maxResults)
	}
	if matches[0].Display != "target.go" || matches[0].IsDir {
		t.Fatalf("first match = %+v, want exact file", matches[0])
	}
	if matches[1].Name != "target.go" || !matches[1].IsDir {
		t.Fatalf("second match = %+v, want exact directory", matches[1])
	}
}

func TestRankFileCandidatesPrefersMatchingUppercase(t *testing.T) {
	cwd := t.TempDir()
	candidates := []fileCandidate{
		{path: filepath.Join(cwd, "agent"), name: "agent", directory: true},
		{path: filepath.Join(cwd, "AGENTS.md"), name: "AGENTS.md"},
	}

	matches := rankFileCandidates(context.Background(), cwd, fileSearchSpec{directory: cwd, query: "AGENT", recursive: true}, candidates)
	if len(matches) != 2 || matches[0].Display != "AGENTS.md" {
		t.Fatalf("matches = %+v, want AGENTS.md first", matches)
	}
}

func TestRankFileCandidatesOrderingIsDeterministic(t *testing.T) {
	cwd := t.TempDir()
	candidates := []fileCandidate{
		{path: filepath.Join(cwd, "z", "target.go"), name: "target.go"},
		{path: filepath.Join(cwd, "a", "target.go"), name: "target.go"},
		{path: filepath.Join(cwd, "target-copy.go"), name: "target-copy.go"},
	}
	spec := fileSearchSpec{directory: cwd, query: "target", recursive: true}
	first := fileMatchDisplays(rankFileCandidates(context.Background(), cwd, spec, candidates))
	slices.Reverse(candidates)
	second := fileMatchDisplays(rankFileCandidates(context.Background(), cwd, spec, candidates))
	if !slices.Equal(first, second) {
		t.Fatalf("rank order changed: first=%q second=%q", first, second)
	}
}

func TestRankFileCandidatesPrefersDirectoriesForEmptyBrowse(t *testing.T) {
	root := t.TempDir()
	candidates := []fileCandidate{
		{path: filepath.Join(root, "file.txt"), name: "file.txt"},
		{path: filepath.Join(root, "folder"), name: "folder", directory: true},
	}
	matches := rankFileCandidates(context.Background(), root, fileSearchSpec{directory: root, explicit: true}, candidates)
	if len(matches) != 2 || !matches[0].IsDir {
		t.Fatalf("matches = %+v, want directory first", matches)
	}
}

func TestRankFileCandidatesMakesCWDDirectoryNavigationExplicit(t *testing.T) {
	root := t.TempDir()
	matches := rankFileCandidates(context.Background(), root, fileSearchSpec{directory: root, recursive: true}, []fileCandidate{{
		path:      filepath.Join(root, "terminal"),
		name:      "terminal",
		directory: true,
	}})
	if len(matches) != 1 || matches[0].BrowseQuery != "./terminal/" {
		t.Fatalf("matches = %+v, want explicit CWD directory navigation", matches)
	}
}

func TestSearcherSearchesCWDAndBrowsesExternalDirectoriesShallowly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := filepath.Join(home, "Code", "eul")
	other := filepath.Join(home, "Code", "other")
	writeSearchFile(t, filepath.Join(cwd, "terminal", "tui.go"), "package terminal")
	writeSearchFile(t, filepath.Join(other, "README.md"), "other")
	writeSearchFile(t, filepath.Join(other, "nested", "deep.txt"), "deep")

	runner := New(cwd)
	defer runner.Close()
	output := runner.Results()

	runner.Search(context.Background(), Request{ID: 1, Query: "tui", Refresh: true})
	cwdResult := waitForFileSearchResult(t, output, 1, func(result Result) bool {
		return result.State == StateComplete
	})
	if len(cwdResult.Matches) != 1 || cwdResult.Matches[0].Display != "terminal/tui.go" || cwdResult.Matches[0].Reference != "terminal/tui.go" {
		t.Fatalf("CWD matches = %+v", cwdResult.Matches)
	}

	runner.Search(context.Background(), Request{ID: 2, Query: "~/Code/"})
	codeResult := waitForFileSearchResult(t, output, 2, func(result Result) bool {
		return result.State == StateComplete
	})
	if got, want := fileMatchDisplays(codeResult.Matches), []string{"~/Code/eul/", "~/Code/other/"}; !slices.Equal(got, want) {
		t.Fatalf("Code matches = %q, want %q", got, want)
	}

	runner.Search(context.Background(), Request{ID: 3, Query: "~/Code/other/"})
	otherResult := waitForFileSearchResult(t, output, 3, func(result Result) bool {
		return result.State == StateComplete
	})
	if got, want := fileMatchDisplays(otherResult.Matches), []string{"~/Code/other/nested/", "~/Code/other/README.md"}; !slices.Equal(got, want) {
		t.Fatalf("external matches = %q, want %q", got, want)
	}
	for _, match := range otherResult.Matches {
		if !filepath.IsAbs(match.Reference) || match.Name == "deep.txt" {
			t.Fatalf("external match = %+v", match)
		}
	}
}

func TestSearcherSearchesSymlinkCWD(t *testing.T) {
	root := t.TempDir()
	realCWD := filepath.Join(root, "real")
	writeSearchFile(t, filepath.Join(realCWD, "file.go"), "package file")
	linkedCWD := filepath.Join(root, "linked")
	if err := os.Symlink(realCWD, linkedCWD); err != nil {
		t.Fatal(err)
	}

	runner := New(linkedCWD)
	defer runner.Close()
	output := runner.Results()
	runner.Search(context.Background(), Request{ID: 1, Query: "file", Refresh: true})
	result := waitForFileSearchResult(t, output, 1, func(result Result) bool {
		return result.State == StateComplete
	})
	if len(result.Matches) != 1 || result.Matches[0].Display != "file.go" || result.Matches[0].Reference != "file.go" {
		t.Fatalf("matches = %+v", result.Matches)
	}
}

func TestSearcherReportsInvalidBrowseRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner := New(home)
	defer runner.Close()
	output := runner.Results()

	runner.Search(context.Background(), Request{ID: 1, Query: "~/missing/", Refresh: true})
	result := waitForFileSearchResult(t, output, 1, func(result Result) bool {
		return result.State == StateFailed
	})
	if result.Err == nil || len(result.Matches) != 0 {
		t.Fatalf("failed result = %+v", result)
	}
}

func TestSearcherReusesDiscoveryAcrossQueryEdits(t *testing.T) {
	cwd := t.TempDir()
	writeSearchFile(t, filepath.Join(cwd, "first.go"), "first")
	runner := New(cwd)
	defer runner.Close()
	output := runner.Results()

	runner.Search(context.Background(), Request{ID: 1, Query: "first", Refresh: true})
	waitForFileSearchResult(t, output, 1, func(result Result) bool {
		return result.State == StateComplete && len(result.Matches) == 1 && result.Matches[0].Name == "first.go"
	})
	writeSearchFile(t, filepath.Join(cwd, "second.go"), "second")

	runner.Search(context.Background(), Request{ID: 2, Query: "second"})
	result := waitForFileSearchResult(t, output, 2, func(result Result) bool {
		return result.State == StateComplete
	})
	if len(result.Matches) != 0 {
		t.Fatalf("query edit rediscovered files: %+v", result.Matches)
	}
}

func TestSearcherRefreshReplacesStaleEntries(t *testing.T) {
	cwd := t.TempDir()
	writeSearchFile(t, filepath.Join(cwd, "old.go"), "old")
	runner := New(cwd)
	defer runner.Close()
	output := runner.Results()

	runner.Search(context.Background(), Request{ID: 1, Query: ".go", Refresh: true})
	waitForFileSearchResult(t, output, 1, func(result Result) bool {
		return result.State == StateComplete && len(result.Matches) == 1 && result.Matches[0].Name == "old.go"
	})
	if err := os.Remove(filepath.Join(cwd, "old.go")); err != nil {
		t.Fatal(err)
	}
	writeSearchFile(t, filepath.Join(cwd, "new.go"), "new")

	runner.Search(context.Background(), Request{ID: 2, Query: ".go", Refresh: true})
	result := waitForFileSearchResult(t, output, 2, func(result Result) bool {
		return result.State == StateComplete
	})
	if len(result.Matches) != 1 || result.Matches[0].Name != "new.go" {
		t.Fatalf("refreshed matches = %+v", result.Matches)
	}
}

func TestSearcherEvictsOldCatalogs(t *testing.T) {
	cwd := t.TempDir()
	for index := range maxCatalogs + 1 {
		if err := os.Mkdir(filepath.Join(cwd, fmt.Sprintf("directory-%02d", index)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := New(cwd)
	defer runner.Close()
	output := runner.Results()
	var id uint64
	search := func(query string) Result {
		id++
		runner.Search(context.Background(), Request{ID: id, Query: query})
		return waitForFileSearchResult(t, output, id, func(result Result) bool {
			return result.State == StateComplete
		})
	}

	search("./directory-00/")
	writeSearchFile(t, filepath.Join(cwd, "directory-00", "late.go"), "late")
	if result := search("./directory-00/late"); len(result.Matches) != 0 {
		t.Fatalf("cached catalog matches = %+v", result.Matches)
	}
	for index := 1; index <= maxCatalogs; index++ {
		search(fmt.Sprintf("./directory-%02d/", index))
	}
	if result := search("./directory-00/late"); len(result.Matches) != 1 || result.Matches[0].Name != "late.go" {
		t.Fatalf("evicted catalog matches = %+v", result.Matches)
	}
}

func TestSearcherDoesNotBlockUpdatesOnUnreadResults(t *testing.T) {
	cwd := t.TempDir()
	writeSearchFile(t, filepath.Join(cwd, "file.go"), "file")
	runner := New(cwd)

	updated := make(chan struct{})
	go func() {
		defer close(updated)
		for id := uint64(1); id <= 1_000; id++ {
			runner.Search(context.Background(), Request{ID: id, Query: fmt.Sprintf("file-%d", id)})
		}
	}()
	select {
	case <-updated:
	case <-time.After(2 * time.Second):
		t.Fatal("updates blocked behind unread search results")
	}

	closed := make(chan struct{})
	go func() {
		runner.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("close blocked behind unread search results")
	}
}

func collectDiscoveredPaths(t *testing.T, spec fileSearchSpec) []string {
	t.Helper()
	var candidates []fileCandidate
	limited, err := discoverFiles(context.Background(), spec, func(batch []fileCandidate) error {
		candidates = append(candidates, batch...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if limited {
		t.Fatal("small discovery was unexpectedly limited")
	}
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		relative, err := filepath.Rel(spec.directory, candidate.path)
		if err != nil {
			t.Fatal(err)
		}
		relative = filepath.ToSlash(relative)
		if candidate.directory {
			relative += "/"
		}
		paths = append(paths, relative)
	}
	slices.Sort(paths)
	return paths
}

func fileMatchDisplays(matches []Match) []string {
	displays := make([]string, len(matches))
	for index, match := range matches {
		displays[index] = match.Display
	}
	return displays
}

func waitForFileSearchResult(t *testing.T, output <-chan Result, id uint64, accept func(Result) bool) Result {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	var received []Result
	for {
		select {
		case result := <-output:
			received = append(received, result)
			if result.ID == id && accept(result) {
				return result
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for file search result %d; received %+v", id, received)
		}
	}
}

func writeSearchFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
