package lsp

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

func (m *watchManager) parseRegistrations(registrations []protocol.Registration) ([]watchRegistration, error) {
	parsed := make([]watchRegistration, 0, len(registrations))
	ids := make(map[string]struct{})
	for _, registration := range registrations {
		if registration.Method != protocol.MethodWorkspaceDidChangeWatchedFiles {
			continue
		}
		if registration.ID == "" {
			return nil, errors.New("watched-files registration has no id")
		}
		if _, duplicate := ids[registration.ID]; duplicate {
			return nil, fmt.Errorf("duplicate watched-files registration %q", registration.ID)
		}
		ids[registration.ID] = struct{}{}

		data, err := protocol.Marshal(registration.RegisterOptions)
		if err != nil {
			return nil, fmt.Errorf("encode watched-files registration %q: %w", registration.ID, err)
		}
		var options protocol.DidChangeWatchedFilesRegistrationOptions
		if err := protocol.Unmarshal(data, &options); err != nil {
			return nil, fmt.Errorf("decode watched-files registration %q: %w", registration.ID, err)
		}
		patterns := make([]watchPattern, 0, len(options.Watchers))
		for _, watcher := range options.Watchers {
			pattern, err := m.parsePattern(watcher)
			if err != nil {
				return nil, fmt.Errorf("watched-files registration %q: %w", registration.ID, err)
			}
			patterns = append(patterns, pattern)
		}
		parsed = append(parsed, watchRegistration{id: registration.ID, patterns: patterns})
	}
	return parsed, nil
}

func (m *watchManager) parsePattern(watcher protocol.FileSystemWatcher) (watchPattern, error) {
	var base string
	var patternValue protocol.Pattern
	switch pattern := watcher.GlobPattern.(type) {
	case protocol.Pattern:
		base = m.folder.URI.FsPath()
		patternValue = pattern
	case *protocol.RelativePattern:
		var baseURI uri.URI
		switch value := pattern.BaseURI.(type) {
		case protocol.URI:
			baseURI = uri.URI(value)
		case *protocol.WorkspaceFolder:
			baseURI = value.URI
		default:
			return watchPattern{}, fmt.Errorf("unsupported relative pattern base %T", pattern.BaseURI)
		}
		if baseURI.Scheme() != "file" {
			return watchPattern{}, fmt.Errorf("unsupported relative pattern base %q", baseURI)
		}
		base = baseURI.FsPath()
		patternValue = pattern.Pattern
	default:
		return watchPattern{}, fmt.Errorf("unsupported glob pattern %T", watcher.GlobPattern)
	}
	if string(patternValue) == "" {
		return watchPattern{}, errors.New("glob pattern is empty")
	}

	glob := filepath.ToSlash(string(patternValue))
	if !filepath.IsAbs(filepath.FromSlash(glob)) {
		glob = path.Join(filepath.ToSlash(base), glob)
	}
	if !doublestar.ValidatePattern(glob) {
		return watchPattern{}, fmt.Errorf("invalid glob pattern %q", patternValue)
	}
	root, err := lspGlobRoot(glob)
	if err != nil {
		return watchPattern{}, err
	}
	workspaceRoot := filepath.Clean(m.folder.URI.FsPath())
	if !lspPathWithin(root, workspaceRoot) {
		return watchPattern{}, fmt.Errorf("glob root %s is outside workspace %s", filepath.ToSlash(root), filepath.ToSlash(workspaceRoot))
	}
	kind := watcher.Kind
	if kind == 0 {
		kind = protocol.WatchKindCreate | protocol.WatchKindChange | protocol.WatchKindDelete
	}
	return watchPattern{glob: glob, root: root, kind: kind}, nil
}

func lspGlobRoot(glob string) (string, error) {
	firstMeta := strings.IndexAny(glob, "*?[{")
	if firstMeta < 0 {
		root := filepath.Dir(filepath.FromSlash(glob))
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("glob pattern %q is not absolute", glob)
		}
		return filepath.Clean(root), nil
	}
	prefix := glob[:firstMeta]
	separator := strings.LastIndex(prefix, "/")
	if separator < 0 {
		return "", fmt.Errorf("glob pattern %q has no absolute root", glob)
	}
	root := filepath.FromSlash(prefix[:separator])
	if root == "" {
		root = string(filepath.Separator)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("glob pattern %q is not absolute", glob)
	}
	return filepath.Clean(root), nil
}
