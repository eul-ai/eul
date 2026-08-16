package filesearch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
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
	cwd := filepath.Join(home, "Code", "eul")
	other := filepath.Join(home, "Code", "other")
	writeSearchFile(t, filepath.Join(cwd, "terminal", "tui.go"), "package terminal")
	writeSearchFile(t, filepath.Join(other, "README.md"), "other")
	writeSearchFile(t, filepath.Join(other, "nested", "deep.txt"), "deep")

	runner := newConfiguredSearcher(cwd, home, resolveFileSearchSpec, discoverFiles)
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

	runner := newConfiguredSearcher(linkedCWD, "", resolveFileSearchSpec, discoverFiles)
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
	runner := newConfiguredSearcher(home, home, resolveFileSearchSpec, discoverFiles)
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
	started := make(chan struct{})
	var calls atomic.Int32
	var resolveCalls atomic.Int32
	resolve := func(cwd, canonicalCWD, home, query string) (fileSearchSpec, error) {
		resolveCalls.Add(1)
		return resolveFileSearchSpec(cwd, canonicalCWD, home, query)
	}
	discover := func(ctx context.Context, spec fileSearchSpec, emit func([]fileCandidate) error) (bool, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		if err := emit([]fileCandidate{
			{path: filepath.Join(spec.directory, "first.go"), name: "first.go"},
			{path: filepath.Join(spec.directory, "second.go"), name: "second.go"},
		}); err != nil {
			return false, err
		}
		<-ctx.Done()
		return false, ctx.Err()
	}
	runner := newConfiguredSearcher(cwd, "", resolve, discover)
	defer runner.Close()
	output := runner.Results()

	runner.Search(context.Background(), Request{ID: 1, Query: "first", Refresh: true})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("discovery did not start")
	}
	waitForFileSearchResult(t, output, 1, func(result Result) bool {
		return len(result.Matches) == 1 && result.Matches[0].Name == "first.go"
	})

	runner.Search(context.Background(), Request{ID: 2, Query: "second"})
	waitForFileSearchResult(t, output, 2, func(result Result) bool {
		return len(result.Matches) == 1 && result.Matches[0].Name == "second.go"
	})
	if got := calls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want 1", got)
	}
	if got := resolveCalls.Load(); got != 1 {
		t.Fatalf("scope resolutions = %d, want 1", got)
	}
}

func TestSearcherRefreshReplacesStaleEntries(t *testing.T) {
	cwd := t.TempDir()
	var calls atomic.Int32
	discover := func(_ context.Context, spec fileSearchSpec, emit func([]fileCandidate) error) (bool, error) {
		name := "old.go"
		if calls.Add(1) > 1 {
			name = "new.go"
		}
		return false, emit([]fileCandidate{{path: filepath.Join(spec.directory, name), name: name}})
	}
	runner := newConfiguredSearcher(cwd, "", resolveFileSearchSpec, discover)
	defer runner.Close()
	output := runner.Results()

	runner.Search(context.Background(), Request{ID: 1, Refresh: true})
	waitForFileSearchResult(t, output, 1, func(result Result) bool {
		return result.State == StateComplete && len(result.Matches) == 1 && result.Matches[0].Name == "old.go"
	})

	runner.Search(context.Background(), Request{ID: 2, Refresh: true})
	result := waitForFileSearchResult(t, output, 2, func(result Result) bool {
		return result.State == StateComplete
	})
	if len(result.Matches) != 1 || result.Matches[0].Name != "new.go" {
		t.Fatalf("refreshed matches = %+v", result.Matches)
	}
}

func TestSearcherCancelsDiscoveryWhenScopeChanges(t *testing.T) {
	cwd := t.TempDir()
	first := filepath.Join(cwd, "first")
	second := filepath.Join(cwd, "second")
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	first, err := filepath.EvalSymlinks(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err = filepath.EvalSymlinks(second)
	if err != nil {
		t.Fatal(err)
	}
	firstStarted := make(chan struct{})
	firstCanceled := make(chan struct{})
	resolve := func(_, _, _, query string) (fileSearchSpec, error) {
		directory := first
		if query == "second" {
			directory = second
		}
		return fileSearchSpec{directory: directory, query: query, recursive: true}, nil
	}
	discover := func(ctx context.Context, spec fileSearchSpec, emit func([]fileCandidate) error) (bool, error) {
		if spec.directory == first {
			close(firstStarted)
			<-ctx.Done()
			close(firstCanceled)
			return false, ctx.Err()
		}
		return false, emit([]fileCandidate{{path: filepath.Join(second, "second.go"), name: "second.go"}})
	}
	runner := newConfiguredSearcher(cwd, "", resolve, discover)
	defer runner.Close()
	output := runner.Results()

	runner.Search(context.Background(), Request{ID: 1, Query: "first", Refresh: true})
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first discovery did not start")
	}
	runner.Search(context.Background(), Request{ID: 2, Query: "second"})
	select {
	case <-firstCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("first discovery was not canceled")
	}
	waitForFileSearchResult(t, output, 2, func(result Result) bool {
		return result.State == StateComplete && len(result.Matches) == 1
	})
}

func TestSearcherCancelStopsActiveDiscovery(t *testing.T) {
	cwd := t.TempDir()
	started := make(chan struct{})
	canceled := make(chan struct{})
	discover := func(ctx context.Context, _ fileSearchSpec, _ func([]fileCandidate) error) (bool, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return false, ctx.Err()
	}
	searcher := newConfiguredSearcher(cwd, "", resolveFileSearchSpec, discover)
	defer searcher.Close()

	searcher.Search(context.Background(), Request{ID: 1, Refresh: true})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("discovery did not start")
	}

	searcher.Cancel()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("discovery was not canceled")
	}
}

func TestSearcherEvictsCatalogsOverEntryBudget(t *testing.T) {
	cwd := t.TempDir()
	directories := []string{
		filepath.Join(cwd, "first"),
		filepath.Join(cwd, "second"),
	}
	resolve := func(_, _, _, query string) (fileSearchSpec, error) {
		var index int
		if _, err := fmt.Sscanf(query, "%d", &index); err != nil {
			return fileSearchSpec{}, err
		}
		return fileSearchSpec{directory: directories[index], query: query}, nil
	}
	var discoveries atomic.Int32
	discover := func(_ context.Context, spec fileSearchSpec, emit func([]fileCandidate) error) (bool, error) {
		discoveries.Add(1)
		return false, emit([]fileCandidate{
			{path: filepath.Join(spec.directory, "file-0.go"), name: "file-0.go"},
			{path: filepath.Join(spec.directory, "file-1.go"), name: "file-1.go"},
		})
	}
	runner := newConfiguredSearcherWithLimits(cwd, "", resolve, discover, cacheLimits{
		maxCatalogs: 10,
		maxEntries:  3,
	})
	defer runner.Close()
	output := runner.Results()

	for id, query := range []string{"0", "1", "0"} {
		requestID := uint64(id + 1)
		runner.Search(context.Background(), Request{ID: requestID, Query: query})
		waitForFileSearchResult(t, output, requestID, func(result Result) bool {
			return result.State == StateComplete
		})
	}
	if got, want := discoveries.Load(), int32(3); got != want {
		t.Fatalf("discoveries = %d, want %d", got, want)
	}
}

func TestSearcherCancelsCompletedDiscoveryContext(t *testing.T) {
	cwd := t.TempDir()
	discoveryContexts := make(chan context.Context, 1)
	discover := func(ctx context.Context, _ fileSearchSpec, _ func([]fileCandidate) error) (bool, error) {
		discoveryContexts <- ctx
		return false, nil
	}
	runner := newConfiguredSearcher(cwd, "", resolveFileSearchSpec, discover)
	defer runner.Close()
	output := runner.Results()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner.Search(parent, Request{ID: 1, Refresh: true})
	var discoveryContext context.Context
	select {
	case discoveryContext = <-discoveryContexts:
	case <-time.After(2 * time.Second):
		t.Fatal("discovery did not start")
	}
	waitForFileSearchResult(t, output, 1, func(result Result) bool {
		return result.State == StateComplete
	})
	select {
	case <-discoveryContext.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("completed discovery context was not canceled")
	}
	if err := parent.Err(); err != nil {
		t.Fatalf("parent context was canceled: %v", err)
	}
}

func TestSearcherEvictsOldCatalogs(t *testing.T) {
	cwd := t.TempDir()
	directories := make([]string, maxCatalogs+1)
	for index := range directories {
		directory := filepath.Join(cwd, fmt.Sprintf("directory-%02d", index))
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		canonical, err := filepath.EvalSymlinks(directory)
		if err != nil {
			t.Fatal(err)
		}
		directories[index] = canonical
	}
	resolve := func(_, _, _, query string) (fileSearchSpec, error) {
		var index int
		if _, err := fmt.Sscanf(query, "%d", &index); err != nil {
			return fileSearchSpec{}, err
		}
		return fileSearchSpec{directory: directories[index], query: query}, nil
	}
	var discoveries atomic.Int32
	discover := func(_ context.Context, _ fileSearchSpec, _ func([]fileCandidate) error) (bool, error) {
		discoveries.Add(1)
		return false, nil
	}
	runner := newConfiguredSearcher(cwd, "", resolve, discover)
	defer runner.Close()
	output := runner.Results()

	var id uint64
	for index := range directories {
		id++
		runner.Search(context.Background(), Request{ID: id, Query: fmt.Sprint(index)})
		waitForFileSearchResult(t, output, id, func(result Result) bool {
			return result.State == StateComplete
		})
	}
	id++
	runner.Search(context.Background(), Request{ID: id, Query: "0"})
	waitForFileSearchResult(t, output, id, func(result Result) bool {
		return result.State == StateComplete
	})
	if got, want := discoveries.Load(), int32(len(directories)+1); got != want {
		t.Fatalf("discoveries = %d, want %d", got, want)
	}
}

func TestSearcherDoesNotBlockUpdatesOnUnreadResults(t *testing.T) {
	cwd := t.TempDir()
	discover := func(_ context.Context, spec fileSearchSpec, emit func([]fileCandidate) error) (bool, error) {
		return false, emit([]fileCandidate{{path: filepath.Join(spec.directory, "file.go"), name: "file.go"}})
	}
	runner := newConfiguredSearcher(cwd, "", resolveFileSearchSpec, discover)

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

func TestSearcherCloseJoinsCanceledDiscovery(t *testing.T) {
	cwd := t.TempDir()
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	discover := func(ctx context.Context, _ fileSearchSpec, _ func([]fileCandidate) error) (bool, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return false, ctx.Err()
	}
	runner := newConfiguredSearcher(cwd, "", resolveFileSearchSpec, discover)
	runner.Search(context.Background(), Request{ID: 1, Refresh: true})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("discovery did not start")
	}

	closed := make(chan struct{})
	go func() {
		runner.Close()
		close(closed)
	}()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("discovery was not canceled")
	}
	select {
	case <-closed:
		t.Fatal("close returned before discovery exited")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("close did not join discovery")
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
