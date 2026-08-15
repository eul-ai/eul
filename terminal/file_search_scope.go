package terminal

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	fileDiscoveryReadBatchSize    = 256
	fileDiscoveryPublishBatchSize = 4_096
	fileDiscoveryPublishInterval  = 50 * time.Millisecond
	fileDiscoveryMaxEntries       = 250_000
	fileDiscoveryMaxDepth         = 64
	fileDiscoveryMaxTime          = 3 * time.Second
)

type fileSearchSpec struct {
	directory     string
	query         string
	prefix        string
	recursive     bool
	includeHidden bool
	explicit      bool
	resolvedScope bool
}

type fileSearchKey struct {
	directory       string
	recursive       bool
	includeHidden   bool
	hiddenTraversal string
}

func (s fileSearchSpec) key() fileSearchKey {
	key := fileSearchKey{
		directory: s.directory,
		recursive: s.recursive,
	}
	if s.recursive && s.includeHidden {
		key.includeHidden = true
		key.hiddenTraversal = hiddenTraversalKey(s.query)
	}
	return key
}

type fileCandidate struct {
	path      string
	name      string
	directory bool
	hidden    bool
}

type discoverFilesFunc func(context.Context, fileSearchSpec, func([]fileCandidate) error) (bool, error)

func resolveFileSearchSpec(cwd, canonicalCWD, home, query string) (fileSearchSpec, error) {
	if !isExplicitFileSearch(query) {
		return fileSearchSpec{
			directory:     canonicalCWD,
			query:         filepath.ToSlash(query),
			recursive:     true,
			includeHidden: queryRequestsHidden(query),
			resolvedScope: true,
		}, nil
	}

	prefix, leaf := splitFileBrowseQuery(query)
	directory, err := expandFileSearchDirectory(cwd, home, prefix)
	if err != nil {
		return fileSearchSpec{}, err
	}
	canonicalDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return fileSearchSpec{}, err
	}
	info, err := os.Stat(canonicalDirectory)
	if err != nil {
		return fileSearchSpec{}, err
	}
	if !info.IsDir() {
		return fileSearchSpec{}, errors.New("search path is not a directory")
	}

	recursive := pathAtOrBelow(canonicalCWD, canonicalDirectory)
	if isShallowFileBrowseRoot(prefix) {
		recursive = false
	}
	return fileSearchSpec{
		directory:     filepath.Clean(canonicalDirectory),
		query:         filepath.ToSlash(leaf),
		prefix:        filepath.ToSlash(prefix),
		recursive:     recursive,
		includeHidden: queryRequestsHidden(leaf),
		explicit:      true,
		resolvedScope: true,
	}, nil
}

func rescoreFileSearchSpec(current fileSearchSpec, query string) (fileSearchSpec, bool) {
	if !current.resolvedScope || isExplicitFileSearch(query) != current.explicit {
		return fileSearchSpec{}, false
	}
	if !current.explicit {
		current.query = filepath.ToSlash(query)
		current.includeHidden = queryRequestsHidden(query)
		return current, true
	}

	prefix, leaf := splitFileBrowseQuery(query)
	prefix = filepath.ToSlash(prefix)
	if prefix != current.prefix {
		return fileSearchSpec{}, false
	}
	current.query = filepath.ToSlash(leaf)
	current.includeHidden = queryRequestsHidden(leaf)
	return current, true
}

func isShallowFileBrowseRoot(prefix string) bool {
	switch filepath.ToSlash(prefix) {
	case "~/", "/", "../":
		return true
	default:
		return false
	}
}

func isExplicitFileSearch(query string) bool {
	switch {
	case query == "~", strings.HasPrefix(query, "~/"):
		return true
	case query == ".", query == "..", strings.HasPrefix(query, "./"), strings.HasPrefix(query, "../"):
		return true
	default:
		return filepath.IsAbs(filepath.FromSlash(query))
	}
}

func splitFileBrowseQuery(query string) (string, string) {
	switch query {
	case "~":
		return "~/", ""
	case ".", "..":
		return query + "/", ""
	}

	index := strings.LastIndex(query, "/")
	if index < 0 {
		return "", query
	}
	return query[:index+1], query[index+1:]
}

func expandFileSearchDirectory(cwd, home, prefix string) (string, error) {
	switch {
	case prefix == "~/" || strings.HasPrefix(prefix, "~/"):
		if home == "" {
			return "", errors.New("home directory is unavailable")
		}
		return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(prefix, "~/"))), nil
	case filepath.IsAbs(filepath.FromSlash(prefix)):
		return filepath.Clean(filepath.FromSlash(prefix)), nil
	default:
		return filepath.Join(cwd, filepath.FromSlash(prefix)), nil
	}
}

func pathAtOrBelow(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || filepath.IsLocal(relative)
}

func queryRequestsHidden(query string) bool {
	if query == "." {
		return true
	}
	for component := range strings.SplitSeq(filepath.ToSlash(query), "/") {
		if strings.HasPrefix(component, ".") && component != "." && component != ".." {
			return true
		}
	}
	return false
}

func hiddenTraversalKey(query string) string {
	components := strings.Split(filepath.ToSlash(query), "/")
	lastHidden := -1
	for index, component := range components {
		if strings.HasPrefix(component, ".") && component != "." && component != ".." {
			lastHidden = index
		}
	}
	if lastHidden < 0 {
		return ""
	}
	return strings.ToLower(strings.Join(components[:lastHidden+1], "/"))
}

func discoverFiles(ctx context.Context, spec fileSearchSpec, emit func([]fileCandidate) error) (bool, error) {
	if pathContainsDotGit(spec.directory) {
		return false, nil
	}

	type queuedDirectory struct {
		path     string
		relative string
		depth    int
		hidden   bool
	}

	started := time.Now()
	lastPublished := started
	queue := []queuedDirectory{{path: spec.directory}}
	batch := make([]fileCandidate, 0, fileDiscoveryPublishBatchSize)
	count := 0
	limited := false
	partial := false

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		published := append([]fileCandidate(nil), batch...)
		batch = batch[:0]
		lastPublished = time.Now()
		return emit(published)
	}

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if count >= fileDiscoveryMaxEntries || time.Since(started) >= fileDiscoveryMaxTime {
			limited = true
			break
		}

		current := queue[0]
		queue[0] = queuedDirectory{}
		queue = queue[1:]
		file, err := os.Open(current.path)
		if err != nil {
			if current.path == spec.directory {
				return false, err
			}
			partial = true
			continue
		}

		for {
			entries, readErr := file.ReadDir(fileDiscoveryReadBatchSize)
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					_ = file.Close()
					return false, err
				}
				if count >= fileDiscoveryMaxEntries || time.Since(started) >= fileDiscoveryMaxTime {
					limited = true
					break
				}
				if !validDiscoveredName(entry.Name()) || entry.Name() == ".git" {
					continue
				}

				hidden := strings.HasPrefix(entry.Name(), ".")
				if hidden && spec.recursive && !spec.includeHidden {
					continue
				}
				entryType := entry.Type()
				if entryType&os.ModeSymlink != 0 {
					continue
				}

				candidate := fileCandidate{
					path:      filepath.Join(current.path, entry.Name()),
					name:      entry.Name(),
					directory: entry.IsDir(),
					hidden:    current.hidden || hidden,
				}
				if !candidate.directory && !entryType.IsRegular() {
					continue
				}

				batch = append(batch, candidate)
				count++
				if candidate.directory && spec.recursive {
					relative := entry.Name()
					if current.relative != "" {
						relative = filepath.Join(current.relative, entry.Name())
					}
					if hidden && !hiddenDirectoryRequested(relative, spec.query) {
						continue
					}
					if current.depth < fileDiscoveryMaxDepth {
						queue = append(queue, queuedDirectory{
							path:     candidate.path,
							relative: relative,
							depth:    current.depth + 1,
							hidden:   candidate.hidden,
						})
					} else {
						partial = true
					}
				}
				if len(batch) >= fileDiscoveryPublishBatchSize || time.Since(lastPublished) >= fileDiscoveryPublishInterval {
					if err := flush(); err != nil {
						_ = file.Close()
						return false, err
					}
				}
			}

			if limited {
				break
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					if current.path == spec.directory {
						_ = file.Close()
						return false, readErr
					}
					partial = true
				}
				break
			}
		}
		if err := file.Close(); err != nil {
			partial = true
		}
		if current.depth == 0 {
			if err := flush(); err != nil {
				return false, err
			}
		}
		if limited {
			break
		}
	}

	if err := flush(); err != nil {
		return false, err
	}
	return limited || partial, nil
}

func hiddenDirectoryRequested(relative, query string) bool {
	relative = strings.ToLower(filepath.ToSlash(relative))
	query = strings.ToLower(filepath.ToSlash(query))
	return query == relative || strings.HasPrefix(query, relative+"/")
}

func pathContainsDotGit(path string) bool {
	path = filepath.Clean(path)
	for {
		if filepath.Base(path) == ".git" {
			return true
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
		path = parent
	}
}

func validDiscoveredName(value string) bool {
	return value != "" && value != "." && value != ".." && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}
