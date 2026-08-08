package terminal

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSearchProjectFilesWithFDIsFreshAndRespectsIgnores(t *testing.T) {
	fdPath := findFD()
	if fdPath == "" {
		t.Skip("fd is not available")
	}

	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePickerFile(t, filepath.Join(cwd, ".gitignore"), "ignored.go\n")
	writePickerFile(t, filepath.Join(cwd, "terminal", "tui.go"), "package terminal")
	writePickerFile(t, filepath.Join(cwd, "ignored.go"), "package ignored")
	writePickerFile(t, filepath.Join(cwd, ".hidden", "config"), "hidden")

	paths, err := searchProjectFiles(context.Background(), cwd, fdPath, "tui")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"terminal/tui.go"}; !slices.Equal(paths, want) {
		t.Fatalf("paths = %q, want %q", paths, want)
	}

	paths, err = searchProjectFiles(context.Background(), cwd, fdPath, "TERMINAL/TUI")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"terminal/tui.go"}; !slices.Equal(paths, want) {
		t.Fatalf("full-path results = %q, want %q", paths, want)
	}

	all, err := searchProjectFiles(context.Background(), cwd, fdPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(all, ".gitignore") || !slices.Contains(all, ".hidden/config") || slices.Contains(all, "ignored.go") {
		t.Fatalf("all paths = %q", all)
	}

	writePickerFile(t, filepath.Join(cwd, "new-file.go"), "package sample")
	fresh, err := searchProjectFiles(context.Background(), cwd, fdPath, "new-file")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"new-file.go"}; !slices.Equal(fresh, want) {
		t.Fatalf("fresh paths = %q, want %q", fresh, want)
	}
}

func TestSearchProjectFilesWithWalkFiltersAndExcludesGit(t *testing.T) {
	cwd := t.TempDir()
	writePickerFile(t, filepath.Join(cwd, ".git", "config"), "metadata")
	writePickerFile(t, filepath.Join(cwd, "terminal", "tui.go"), "package terminal")
	writePickerFile(t, filepath.Join(cwd, "terminal", "model.go"), "package terminal")

	paths, err := searchProjectFiles(context.Background(), cwd, "", "tui")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"terminal/tui.go"}; !slices.Equal(paths, want) {
		t.Fatalf("paths = %q, want %q", paths, want)
	}

	paths, err = searchProjectFiles(context.Background(), cwd, "", "terminal/model")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"terminal/model.go"}; !slices.Equal(paths, want) {
		t.Fatalf("full-path results = %q, want %q", paths, want)
	}
}

func TestSearchProjectFilesWithWalkExcludesGitPointerFile(t *testing.T) {
	cwd := t.TempDir()
	writePickerFile(t, filepath.Join(cwd, ".git"), "gitdir: ../metadata")
	writePickerFile(t, filepath.Join(cwd, "file.go"), "package sample")

	paths, err := searchProjectFiles(context.Background(), cwd, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"file.go"}; !slices.Equal(paths, want) {
		t.Fatalf("paths = %q, want %q", paths, want)
	}
}

func TestFilePickerSearchesHomeDirectory(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	writePickerFile(t, filepath.Join(cwd, "notes.txt"), "project notes")
	writePickerFile(t, filepath.Join(home, "notes.txt"), "home notes")

	model := newTUIModel(80, 24, Options{WorkingDirectory: cwd})
	if err := model.insertInput("@~"); err != nil {
		t.Fatal(err)
	}
	request := takePickerRequest(t, model)
	if request.query != "" || request.root != fileSearchHome {
		t.Fatalf("request = %+v, want home search", request)
	}

	runner := &fileSearchRunner{cwd: cwd, home: home}
	paths, err := runner.search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"~/notes.txt"}; !slices.Equal(paths, want) {
		t.Fatalf("paths = %q, want %q", paths, want)
	}

	model.applyFileSearchResult(fileSearchResult{id: request.id, paths: paths})
	if err := model.applyFilePickerSelection(); err != nil {
		t.Fatal(err)
	}
	if got, want := string(model.input), "@~/notes.txt "; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
}

func TestFilePickerSearchesAbsolutePaths(t *testing.T) {
	model := newTUIModel(80, 24, Options{WorkingDirectory: t.TempDir()})
	if err := model.insertInput("@/var/log"); err != nil {
		t.Fatal(err)
	}
	request := takePickerRequest(t, model)
	if request.query != "var/log" || request.root != fileSearchAbsolute {
		t.Fatalf("request = %+v, want absolute query %q", request, "var/log")
	}

	model.applyFileSearchResult(fileSearchResult{id: request.id, paths: []string{"/var/log/system.log"}})
	if err := model.applyFilePickerSelection(); err != nil {
		t.Fatal(err)
	}
	if got, want := string(model.input), "@/var/log/system.log "; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
}

func TestSearchProjectFilesWithWalkHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := searchProjectFiles(ctx, t.TempDir(), "", ""); err != context.Canceled {
		t.Fatalf("error = %v, want context canceled", err)
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
	model := newTUIModel(80, 24, Options{WorkingDirectory: t.TempDir()})
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
	if got, want := string(model.input), "inspect @"+selected+" "; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	if model.filePickerVisible() || model.cursor != len(model.input) {
		t.Fatalf("picker remained open or cursor misplaced: picker=%+v cursor=%d", model.filePicker, model.cursor)
	}
}

func TestFilePickerRejectsStaleSearchResults(t *testing.T) {
	model := newTUIModel(80, 24, Options{WorkingDirectory: t.TempDir()})
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
	model := newTUIModel(80, 24, Options{WorkingDirectory: t.TempDir()})
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
	if got, want := string(model.input), `@"my folder/file.go" `; got != want {
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
