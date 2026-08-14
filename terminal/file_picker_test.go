package terminal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eul-ai/eul/agent"
)

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
