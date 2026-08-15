package terminal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestFilePickerKeepsSearchQueriesLiteral(t *testing.T) {
	for _, query := range []string{"~", "~/Code", "/var/log", "../other"} {
		model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: t.TempDir()}})
		if err := model.insertInput("@" + query); err != nil {
			t.Fatal(err)
		}
		request := takePickerRequest(t, model)
		if request.query != query || !request.refresh {
			t.Fatalf("request = %+v, want initial query %q with refresh", request, query)
		}
	}
}

func TestFileReferenceTokenParsesCompleteAndQuotedToken(t *testing.T) {
	tests := []struct {
		input  string
		cursor int
		start  int
		end    int
		query  string
		ok     bool
	}{
		{input: "@", cursor: 1, start: 0, end: 1, ok: true},
		{input: "inspect @tui", cursor: len([]rune("inspect @tui")), start: 8, end: 12, query: "tui", ok: true},
		{input: "first\n@read", cursor: len([]rune("first\n@read")), start: 6, end: 11, query: "read", ok: true},
		{input: "say @foo/bar then", cursor: len([]rune("say @fo")), start: 4, end: 12, query: "foo/bar", ok: true},
		{input: `inspect @"my folder/file.go"`, cursor: len([]rune(`inspect @"my folder/file.go"`)), start: 8, end: 28, query: "my folder/file.go", ok: true},
		{input: "mail@example.com", cursor: len([]rune("mail@example.com"))},
		{input: "(@file", cursor: len([]rune("(@file"))},
	}
	for _, test := range tests {
		input := []rune(test.input)
		start, end, query, ok := fileReferenceToken(input, test.cursor)
		if start != test.start || end != test.end || query != test.query || ok != test.ok {
			t.Fatalf("token for %q at %d = %d,%d,%q,%t, want %d,%d,%q,%t", test.input, test.cursor, start, end, query, ok, test.start, test.end, test.query, test.ok)
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
	if !model.applyFileSearchResult(testFileSearchResult(request.id, "terminal/tui.go", "terminal/tui_model.go")) {
		t.Fatal("current search result was rejected")
	}

	model.moveFilePickerSelection(1)
	selected := model.filePicker.matches[model.filePicker.selected]
	if err := model.applyFilePickerSelection(); err != nil {
		t.Fatal(err)
	}
	if got, want := model.inputText(), "inspect @"+selected.reference+" "; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	if model.filePickerVisible() || model.cursor != len(model.input) {
		t.Fatalf("picker remained open or cursor misplaced: picker=%+v cursor=%d", model.filePicker, model.cursor)
	}
}

func TestFilePickerPreservesSelectionAcrossSearchUpdates(t *testing.T) {
	model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: t.TempDir()}})
	if err := model.insertInput("@"); err != nil {
		t.Fatal(err)
	}
	request := takePickerRequest(t, model)
	model.applyFileSearchResult(fileSearchResult{id: request.id, matches: testFileSearchMatches("a.go", "b.go"), state: fileSearchDiscovering})
	model.moveFilePickerSelection(1)
	selected := model.filePicker.matches[model.filePicker.selected].identity()

	model.applyFileSearchResult(fileSearchResult{id: request.id, matches: testFileSearchMatches("b.go", "c.go"), state: fileSearchComplete})
	if got := model.filePicker.matches[model.filePicker.selected].identity(); got != selected {
		t.Fatalf("selected match = %q, want %q", got, selected)
	}
}

func TestFilePickerReplacesWholeTokenFromMiddle(t *testing.T) {
	model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: t.TempDir()}})
	if err := model.insertInput("inspect @foo.go next"); err != nil {
		t.Fatal(err)
	}
	model.cursor = len([]rune("inspect @fo"))
	model.refreshFilePicker(true)
	request := takePickerRequest(t, model)
	model.applyFileSearchResult(testFileSearchResult(request.id, "foo_test.go"))
	if err := model.applyFilePickerSelection(); err != nil {
		t.Fatal(err)
	}
	if got, want := model.inputText(), "inspect @foo_test.go next"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
}

func TestFilePickerDrillsIntoDirectory(t *testing.T) {
	model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: t.TempDir()}})
	if err := model.insertInput("@../ot"); err != nil {
		t.Fatal(err)
	}
	request := takePickerRequest(t, model)
	match := testFileSearchMatches("../other/")[0]
	match.reference = "/tmp/other/"
	model.applyFileSearchResult(fileSearchResult{id: request.id, matches: []fileSearchMatch{match}, state: fileSearchComplete})

	if err := model.drillIntoFilePickerDirectory(); err != nil {
		t.Fatal(err)
	}
	if got, want := model.inputText(), "@../other/"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	next := takePickerRequest(t, model)
	if next.query != "../other/" || next.refresh {
		t.Fatalf("drill request = %+v", next)
	}
}

func TestFilePickerRemovesStaleSelection(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "gone.go")
	writePickerFile(t, path, "package gone")
	model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: cwd}})
	if err := model.insertInput("@gone"); err != nil {
		t.Fatal(err)
	}
	request := takePickerRequest(t, model)
	match := testFileSearchMatches("gone.go")[0]
	match.path = path
	model.applyFileSearchResult(fileSearchResult{id: request.id, matches: []fileSearchMatch{match}, state: fileSearchComplete})
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if err := model.applyFilePickerSelection(); err == nil {
		t.Fatal("stale selection succeeded")
	}
	if len(model.filePicker.matches) != 0 || model.inputText() != "@gone" {
		t.Fatalf("stale picker = %+v input=%q", model.filePicker, model.inputText())
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
	model.applyFileSearchResult(testFileSearchResult(request.id, "terminal/tui.go"))

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
	if model.applyFileSearchResult(testFileSearchResult(first.id, "stale.go")) {
		t.Fatal("stale search result was accepted")
	}
	if !model.applyFileSearchResult(testFileSearchResult(second.id, "terminal/tui.go")) {
		t.Fatal("current search result was rejected")
	}
}

func TestFormatFileReferenceQuotesUnicodeWhitespaceAndBackslashes(t *testing.T) {
	if got, want := formatFileReference("my\u00a0folder/file.go"), "@\"my\u00a0folder/file.go\""; got != want {
		t.Fatalf("reference = %q, want %q", got, want)
	}
	if got, want := formatFileReference(`folder\file".go`), `@"folder\\file\".go"`; got != want {
		t.Fatalf("escaped reference = %q, want %q", got, want)
	}
	for _, value := range []string{`folder\`, `folder"`, `folder\"`, "my folder/file.go"} {
		formatted := formatFileReference(value)
		decoded := decodeFileReferenceQuery(formatted[1:])
		if decoded != value {
			t.Fatalf("round trip for %q = %q via %q", value, decoded, formatted)
		}
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
	model.applyFileSearchResult(testFileSearchResult(request.id, "my folder/file.go"))
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
