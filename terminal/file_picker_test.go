package terminal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
)

func TestSearchProjectFilesIsFreshAndAppliesPolicy(t *testing.T) {
	cwd := t.TempDir()
	writePickerFile(t, filepath.Join(cwd, ".git", "config"), "metadata")
	writePickerFile(t, filepath.Join(cwd, ".gitignore"), "ignored.go\n")
	writePickerFile(t, filepath.Join(cwd, ".hidden", "config"), "hidden")
	writePickerFile(t, filepath.Join(cwd, "ignored.go"), "package ignored")
	writePickerFile(t, filepath.Join(cwd, "terminal", "tui.go"), "package terminal")

	paths, err := searchProjectFiles(context.Background(), cwd, "tui")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"terminal/tui.go"}; !slices.Equal(paths, want) {
		t.Fatalf("paths = %q, want %q", paths, want)
	}

	paths, err = searchProjectFiles(context.Background(), cwd, "terminal")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"terminal/"}; !slices.Equal(paths, want) {
		t.Fatalf("directory paths = %q, want %q", paths, want)
	}

	paths, err = searchProjectFiles(context.Background(), cwd, "TERMINAL/TUI")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"terminal/tui.go"}; !slices.Equal(paths, want) {
		t.Fatalf("full-path results = %q, want %q", paths, want)
	}

	all, err := searchProjectFiles(context.Background(), cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ignored.go", "terminal/", "terminal/tui.go"}; !slices.Equal(all, want) {
		t.Fatalf("all paths = %q, want %q", all, want)
	}

	writePickerFile(t, filepath.Join(cwd, "new-file.go"), "package sample")
	fresh, err := searchProjectFiles(context.Background(), cwd, "new-file")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"new-file.go"}; !slices.Equal(fresh, want) {
		t.Fatalf("fresh paths = %q, want %q", fresh, want)
	}
}

func TestSearchProjectFilesReturnsLexicalResultLimit(t *testing.T) {
	cwd := t.TempDir()
	for index := 119; index >= 0; index-- {
		writePickerFile(t, filepath.Join(cwd, fmt.Sprintf("file-%03d.go", index)), "package sample")
	}

	paths, err := searchProjectFiles(context.Background(), cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, filePickerMaxResults)
	for index := range want {
		want[index] = fmt.Sprintf("file-%03d.go", index)
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("paths = %q, want %q", paths, want)
	}
}

func TestSearchProjectFilesExcludesGitPointerFile(t *testing.T) {
	cwd := t.TempDir()
	writePickerFile(t, filepath.Join(cwd, ".git"), "gitdir: ../metadata")
	writePickerFile(t, filepath.Join(cwd, "file.go"), "package sample")

	paths, err := searchProjectFiles(context.Background(), cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"file.go"}; !slices.Equal(paths, want) {
		t.Fatalf("paths = %q, want %q", paths, want)
	}
}

func TestFilePickerKeepsSearchQueriesLiteral(t *testing.T) {
	for _, query := range []string{"~", "~/Code", "/var/log"} {
		model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: t.TempDir()}})
		if err := model.insertInput("@" + query); err != nil {
			t.Fatal(err)
		}
		request := takePickerRequest(t, model)
		if request.query != query {
			t.Fatalf("query = %q, want %q", request.query, query)
		}
	}
}

func TestSearchProjectFilesStaysWithinWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	writePickerFile(t, filepath.Join(outside, "outside.txt"), "outside")
	if err := os.Symlink(outside, filepath.Join(cwd, "link")); err != nil {
		t.Fatal(err)
	}

	paths, err := searchProjectFiles(context.Background(), cwd, "outside")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("paths = %q, want no files outside working directory", paths)
	}
}

func TestSearchProjectFilesHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := searchProjectFiles(ctx, t.TempDir(), ""); err != context.Canceled {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestFileSearchRunnerDebouncesSupersededRequests(t *testing.T) {
	queries := make(chan string, 2)
	runner := &fileSearchRunner{
		debounce: 25 * time.Millisecond,
		search: func(_ context.Context, _, query string) ([]string, error) {
			queries <- query
			return []string{query + ".go"}, nil
		},
	}
	defer runner.close()
	output := make(chan fileSearchResult, 2)

	runner.update(context.Background(), fileSearchCommand{request: &fileSearchRequest{id: 1, query: "first"}}, output)
	runner.update(context.Background(), fileSearchCommand{request: &fileSearchRequest{id: 2, query: "second"}}, output)

	select {
	case result := <-output:
		if result.id != 2 || !slices.Equal(result.paths, []string{"second.go"}) {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("debounced search did not finish")
	}
	select {
	case query := <-queries:
		if query != "second" {
			t.Fatalf("search query = %q, want second", query)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("search did not run")
	}
	select {
	case query := <-queries:
		t.Fatalf("superseded search ran with query %q", query)
	case <-time.After(75 * time.Millisecond):
	}
}

func TestFileSearchRunnerCancelsSupersededSearch(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	runner := &fileSearchRunner{
		search: func(ctx context.Context, _, query string) ([]string, error) {
			if query == "first" {
				close(started)
				<-ctx.Done()
				close(canceled)
				return nil, ctx.Err()
			}
			return []string{"second.go"}, nil
		},
	}
	defer runner.close()
	output := make(chan fileSearchResult, 2)

	runner.update(context.Background(), fileSearchCommand{request: &fileSearchRequest{id: 1, query: "first"}}, output)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first search did not start")
	}
	runner.update(context.Background(), fileSearchCommand{request: &fileSearchRequest{id: 2, query: "second"}}, output)
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("first search was not canceled")
	}
	select {
	case result := <-output:
		if result.id != 2 || !slices.Equal(result.paths, []string{"second.go"}) {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second search did not finish")
	}
}

func TestFileSearchRunnerCloseJoinsPreviouslyCanceledSearch(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	runner := &fileSearchRunner{
		search: func(ctx context.Context, _, query string) ([]string, error) {
			if query != "first" {
				return []string{"second.go"}, nil
			}
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			return nil, ctx.Err()
		},
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		runner.close()
	})
	output := make(chan fileSearchResult, 2)

	runner.update(context.Background(), fileSearchCommand{request: &fileSearchRequest{id: 1, query: "first"}}, output)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first search did not start")
	}
	runner.update(context.Background(), fileSearchCommand{request: &fileSearchRequest{id: 2, query: "second"}}, output)
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("first search was not canceled")
	}
	select {
	case result := <-output:
		if result.id != 2 {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second search did not finish")
	}

	closed := make(chan struct{})
	go func() {
		runner.close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("close returned before the canceled search exited")
	case <-time.After(75 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("close did not join the canceled search")
	}
}

func TestFileReferenceTokenRequiresTokenBoundary(t *testing.T) {
	for _, test := range []struct {
		input string
		start int
		query string
		ok    bool
	}{
		{input: "@", start: 0, ok: true},
		{input: "inspect @tui", start: 8, query: "tui", ok: true},
		{input: "first\n@read", start: 6, query: "read", ok: true},
		{input: "mail@example.com"},
		{input: "(@file"},
	} {
		input := []rune(test.input)
		start, query, ok := fileReferenceToken(input, len(input))
		if start != test.start || query != test.query || ok != test.ok {
			t.Fatalf("token for %q = %d,%q,%t, want %d,%q,%t", test.input, start, query, ok, test.start, test.query, test.ok)
		}
	}
}

func TestFilePickerRequestsNavigatesAndAppliesSelection(t *testing.T) {
	model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: t.TempDir()}})
	if err := model.insertInput("inspect @tui"); err != nil {
		t.Fatal(err)
	}
	request := takePickerRequest(t, model)
	if request.query != "tui" || !model.filePickerVisible() {
		t.Fatalf("request = %+v picker = %+v", request, model.filePicker)
	}
	if !model.applyFileSearchResult(fileSearchResult{id: request.id, paths: []string{"terminal/tui.go", "terminal/tui_model.go"}}) {
		t.Fatal("current search result was rejected")
	}

	model.moveFilePickerSelection(1)
	selected := model.filePicker.matches[model.filePicker.selected]
	if err := model.applyFilePickerSelection(); err != nil {
		t.Fatal(err)
	}
	if got, want := model.inputText(), "inspect @"+selected+" "; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	if model.filePickerVisible() || model.cursor != len(model.input) {
		t.Fatalf("picker remained open or cursor misplaced: picker=%+v cursor=%d", model.filePicker, model.cursor)
	}
}

func TestFilePickerReplacementStopsBeforeImage(t *testing.T) {
	model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: t.TempDir()}})
	if err := model.insertInput("@tu"); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("image")}); err != nil {
		t.Fatal(err)
	}
	model.cursor = len(editorItemsFromText("@tu"))
	model.refreshFilePicker(true)
	request := takePickerRequest(t, model)
	model.applyFileSearchResult(fileSearchResult{id: request.id, paths: []string{"terminal/tui.go"}})

	if err := model.applyFilePickerSelection(); err != nil {
		t.Fatal(err)
	}
	content := editorContent(model.input)
	if len(content) != 2 || content[0].Text != "@terminal/tui.go " || content[1].Kind != agent.ContentPartImage {
		t.Fatalf("content = %+v", content)
	}
}

func TestFilePickerRejectsStaleSearchResults(t *testing.T) {
	model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: t.TempDir()}})
	if err := model.insertInput("@"); err != nil {
		t.Fatal(err)
	}
	first := takePickerRequest(t, model)
	if err := model.insertInput("term"); err != nil {
		t.Fatal(err)
	}
	second := takePickerRequest(t, model)
	if model.applyFileSearchResult(fileSearchResult{id: first.id, paths: []string{"stale.go"}}) {
		t.Fatal("stale search result was accepted")
	}
	if !model.applyFileSearchResult(fileSearchResult{id: second.id, paths: []string{"terminal/tui.go"}}) {
		t.Fatal("current search result was rejected")
	}
}

func TestFormatFileReferenceQuotesUnicodeWhitespace(t *testing.T) {
	if got, want := formatFileReference("my\u00a0folder/file.go"), "@\"my\u00a0folder/file.go\""; got != want {
		t.Fatalf("reference = %q, want %q", got, want)
	}
}

func TestFilePickerQuotesSpacesAndCanBeDismissed(t *testing.T) {
	model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: t.TempDir()}})
	if err := model.insertInput("@"); err != nil {
		t.Fatal(err)
	}
	_ = takePickerRequest(t, model)
	model.dismissFilePicker()
	model.refreshFilePicker(false)
	if model.filePickerVisible() {
		t.Fatal("dismissed picker reopened without an edit")
	}

	if err := model.insertInput("my"); err != nil {
		t.Fatal(err)
	}
	request := takePickerRequest(t, model)
	model.applyFileSearchResult(fileSearchResult{id: request.id, paths: []string{"my folder/file.go"}})
	if !model.filePickerVisible() {
		t.Fatal("picker did not reopen after editing")
	}
	if err := model.applyFilePickerSelection(); err != nil {
		t.Fatal(err)
	}
	if got, want := model.inputText(), `@"my folder/file.go" `; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
}

func takePickerRequest(t *testing.T, model *tuiModel) fileSearchRequest {
	t.Helper()
	command := model.takeFileSearchCommand()
	if command.request == nil {
		t.Fatal("missing file search request")
	}
	return *command.request
}

func writePickerFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
