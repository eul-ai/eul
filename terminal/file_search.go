package terminal

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	filePickerMaxResults = 100
	fileSearchDebounce   = 75 * time.Millisecond
)

var errFileSearchComplete = errors.New("file search complete")

type fileSearchRequest struct {
	id    uint64
	query string
}

type fileSearchResult struct {
	id    uint64
	paths []string
}

type fileSearchCommand struct {
	request *fileSearchRequest
	cancel  bool
}

type projectFileSearch func(context.Context, string, string) ([]string, error)

type fileSearchRunner struct {
	cwd      string
	debounce time.Duration
	search   projectFileSearch
	cancel   context.CancelFunc
	wait     sync.WaitGroup
}

func newFileSearchRunner(cwd string) *fileSearchRunner {
	return &fileSearchRunner{
		cwd:      cwd,
		debounce: fileSearchDebounce,
		search:   searchProjectFiles,
	}
}

func (r *fileSearchRunner) update(ctx context.Context, command fileSearchCommand, output chan<- fileSearchResult) {
	if command.request == nil && !command.cancel {
		return
	}
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if command.request == nil {
		return
	}

	searchContext, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	request := *command.request
	r.wait.Add(1)
	go func() {
		defer r.wait.Done()

		timer := time.NewTimer(r.debounce)
		defer timer.Stop()
		select {
		case <-searchContext.Done():
			return
		case <-timer.C:
		}

		paths, err := r.search(searchContext, r.cwd, request.query)
		if err != nil {
			if searchContext.Err() != nil {
				return
			}
			paths = nil
		}
		if searchContext.Err() != nil {
			return
		}
		select {
		case output <- fileSearchResult{id: request.id, paths: paths}:
		case <-searchContext.Done():
		}
	}()
}

func (r *fileSearchRunner) close() {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.wait.Wait()
}

func searchProjectFiles(ctx context.Context, cwd, query string) ([]string, error) {
	var paths []string
	normalizedQuery := strings.ToLower(filepath.ToSlash(query))
	fullPath := strings.Contains(normalizedQuery, "/")
	err := filepath.WalkDir(cwd, func(filePath string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if filePath == cwd {
				return walkErr
			}
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if filePath != cwd && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
		}

		relative, err := filepath.Rel(cwd, filePath)
		if err != nil {
			return nil
		}
		display, ok := normalizeDiscoveredPath(relative)
		if !ok {
			return nil
		}
		if entry.IsDir() {
			display += "/"
		}
		candidate := path.Base(display)
		if fullPath {
			candidate = display
		}
		if normalizedQuery != "" && !strings.Contains(strings.ToLower(candidate), normalizedQuery) {
			return nil
		}

		paths = append(paths, display)
		if len(paths) >= filePickerMaxResults {
			return errFileSearchComplete
		}
		return nil
	})
	if errors.Is(err, errFileSearchComplete) {
		err = nil
	}
	return paths, err
}

func normalizeDiscoveredPath(value string) (string, bool) {
	if value == "" || !utf8.ValidString(value) {
		return "", false
	}
	value = strings.TrimPrefix(filepath.ToSlash(value), "./")
	if value == "" || value == "." || value == ".." || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") {
		return "", false
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", false
	}
	return value, true
}
