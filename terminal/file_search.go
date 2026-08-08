package terminal

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const filePickerMaxResults = 100

var errFileSearchComplete = errors.New("file search complete")

type fileSearchRoot uint8

const (
	fileSearchProject fileSearchRoot = iota
	fileSearchHome
	fileSearchAbsolute
)

type fileSearchRequest struct {
	id    uint64
	query string
	root  fileSearchRoot
}

type fileSearchResult struct {
	id    uint64
	paths []string
}

type fileSearchCommand struct {
	request *fileSearchRequest
	cancel  bool
}

type fileSearchRunner struct {
	cwd    string
	home   string
	fdPath string
	cancel context.CancelFunc
}

func findFD() string {
	for _, name := range []string{"fd", "fdfind"} {
		if executable, err := exec.LookPath(name); err == nil {
			return executable
		}
	}
	return ""
}

func newFileSearchRunner(cwd string) *fileSearchRunner {
	runner := &fileSearchRunner{cwd: cwd}
	if cwd != "" {
		runner.home, _ = os.UserHomeDir()
		runner.fdPath = findFD()
	}
	return runner
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
	go func() {
		paths, err := r.search(searchContext, request)
		if err != nil {
			if searchContext.Err() != nil {
				return
			}
			paths = nil
		}
		select {
		case output <- fileSearchResult{id: request.id, paths: paths}:
		case <-searchContext.Done():
		}
	}()
}

func (r *fileSearchRunner) search(ctx context.Context, request fileSearchRequest) ([]string, error) {
	directory := r.cwd
	prefix := ""
	switch request.root {
	case fileSearchHome:
		directory = r.home
		prefix = "~/"
	case fileSearchAbsolute:
		directory = "/"
		prefix = "/"
	}

	paths, err := searchProjectFiles(ctx, directory, r.fdPath, request.query)
	if err != nil || prefix == "" {
		return paths, err
	}
	for index := range paths {
		paths[index] = prefix + paths[index]
	}
	return paths, nil
}

func (r *fileSearchRunner) close() {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

func searchProjectFiles(ctx context.Context, cwd, fdPath, query string) ([]string, error) {
	searchPath := fileSearchPath(cwd, query)
	if fdPath != "" {
		return searchProjectFilesWithFD(ctx, cwd, fdPath, searchPath, query)
	}
	return searchProjectFilesWithWalk(ctx, cwd, searchPath, query)
}

func fileSearchPath(cwd, query string) string {
	if !strings.Contains(filepath.ToSlash(query), "/") {
		return ""
	}

	candidate := filepath.Clean(filepath.FromSlash(query))
	for candidate != "." && filepath.IsLocal(candidate) {
		info, err := os.Stat(filepath.Join(cwd, candidate))
		if err == nil && info.IsDir() && pathHasExactComponents(cwd, candidate) {
			return candidate
		}

		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
		candidate = parent
	}
	return ""
}

func pathHasExactComponents(cwd, relative string) bool {
	current := cwd
	for component := range strings.SplitSeq(relative, string(filepath.Separator)) {
		entries, err := os.ReadDir(current)
		if err != nil {
			return false
		}

		found := false
		for _, entry := range entries {
			if entry.Name() == component {
				found = true
				break
			}
		}
		if !found {
			return false
		}
		current = filepath.Join(current, component)
	}
	return true
}

func searchProjectFilesWithFD(ctx context.Context, cwd, fdPath, searchPath, query string) ([]string, error) {
	arguments := []string{
		"--base-directory", cwd,
		"--max-results", "100",
		"--type", "f",
		"--hidden",
		"--follow",
		"--exclude", ".git",
		"--print0",
		"--fixed-strings",
		"--ignore-case",
	}
	if strings.Contains(query, "/") {
		arguments = append(arguments, "--full-path")
	}
	if searchPath != "" {
		arguments = append(arguments, "--search-path", searchPath)
	}
	if query != "" {
		arguments = append(arguments, "--", query)
	}

	command := exec.CommandContext(ctx, fdPath, arguments...)
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	return parseFDPaths(output), nil
}

func parseFDPaths(output []byte) []string {
	parts := bytes.Split(output, []byte{0})
	paths := make([]string, 0, min(len(parts), filePickerMaxResults))
	for _, part := range parts {
		if normalized, ok := normalizeDiscoveredPath(string(part)); ok {
			paths = append(paths, normalized)
			if len(paths) >= filePickerMaxResults {
				break
			}
		}
	}
	return paths
}

func searchProjectFilesWithWalk(ctx context.Context, cwd, searchPath, query string) ([]string, error) {
	var paths []string
	normalizedQuery := strings.ToLower(filepath.ToSlash(query))
	fullPath := strings.Contains(normalizedQuery, "/")
	searchRoot := cwd
	if searchPath != "" {
		searchRoot = filepath.Join(cwd, searchPath)
	}
	err := filepath.WalkDir(searchRoot, func(filePath string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if filePath == searchRoot {
				return walkErr
			}
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if filePath != cwd && entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}

		relative, err := filepath.Rel(cwd, filePath)
		if err != nil {
			return nil
		}
		display, ok := normalizeDiscoveredPath(relative)
		if !ok {
			return nil
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
