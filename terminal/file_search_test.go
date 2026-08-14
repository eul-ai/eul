package terminal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
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
