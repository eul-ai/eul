package filesearch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
