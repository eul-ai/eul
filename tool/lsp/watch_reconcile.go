package lsp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fsnotify/fsnotify"
)

func (state *lspWatchState) reconcile(ctx context.Context) error {
	if err := state.contextError(ctx); err != nil {
		return err
	}

	roots := state.roots()
	directories := make(map[string]struct{})
	known := make(map[string]lspWatchedPathState)
	for _, root := range roots {
		if err := state.contextError(ctx); err != nil {
			return err
		}
		watchRoot, recursive, err := existingLSPWatchRoot(root)
		if err != nil {
			return err
		}
		if !recursive {
			info, err := os.Stat(watchRoot)
			if err != nil {
				return err
			}
			directories[watchRoot] = struct{}{}
			known[watchRoot] = lspPathState(info)
			continue
		}
		err = filepath.WalkDir(watchRoot, func(name string, entry fs.DirEntry, walkErr error) error {
			if err := state.contextError(ctx); err != nil {
				return err
			}
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			clean := filepath.Clean(name)
			if entry.IsDir() {
				directories[clean] = struct{}{}
			}
			known[clean] = lspPathState(info)
			return nil
		})
		if err != nil {
			return fmt.Errorf("scan watched root %s: %w", filepath.ToSlash(watchRoot), err)
		}
	}

	active := maps.Clone(state.watchedDirs)
	added := make([]string, 0)
	directoryNames := make([]string, 0, len(directories))
	for directory := range directories {
		directoryNames = append(directoryNames, directory)
	}
	sort.Strings(directoryNames)
	for _, directory := range directoryNames {
		if _, exists := active[directory]; exists {
			continue
		}
		if err := state.contextError(ctx); err != nil {
			state.watchedDirs = active
			return err
		}
		if err := state.manager.native.Add(directory); err != nil {
			for _, addedDirectory := range added {
				_ = state.manager.native.Remove(addedDirectory)
				delete(active, addedDirectory)
			}
			state.watchedDirs = active
			return fmt.Errorf("watch directory %s: %w", filepath.ToSlash(directory), err)
		}
		active[directory] = struct{}{}
		added = append(added, directory)
	}
	activeNames := make([]string, 0, len(active))
	for directory := range active {
		activeNames = append(activeNames, directory)
	}
	sort.Strings(activeNames)
	for _, directory := range activeNames {
		if _, needed := directories[directory]; needed {
			continue
		}
		if err := state.contextError(ctx); err != nil {
			state.watchedDirs = active
			return err
		}
		if err := state.manager.native.Remove(directory); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) && !errors.Is(err, fsnotify.ErrClosed) {
			state.watchedDirs = active
			return fmt.Errorf("stop watching directory %s: %w", filepath.ToSlash(directory), err)
		}
		delete(active, directory)
	}
	state.watchedDirs = active
	state.known = known
	return nil
}

func (state *lspWatchState) roots() []string {
	rootSet := make(map[string]struct{})
	for _, patterns := range state.registrations {
		for _, pattern := range patterns {
			rootSet[pattern.root] = struct{}{}
		}
	}
	roots := make([]string, 0, len(rootSet))
	for candidate := range rootSet {
		covered := false
		for other := range rootSet {
			if candidate != other && lspPathWithin(candidate, other) {
				covered = true
				break
			}
		}
		if !covered {
			roots = append(roots, candidate)
		}
	}
	sort.Strings(roots)
	return roots
}

func existingLSPWatchRoot(root string) (string, bool, error) {
	requested := filepath.Clean(root)
	candidate := requested
	for {
		info, err := os.Stat(candidate)
		if err == nil {
			if !info.IsDir() {
				return "", false, fmt.Errorf("watch root %s is not a directory", filepath.ToSlash(candidate))
			}
			return candidate, candidate == requested, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", false, err
		}
		candidate = parent
	}
}

func lspPathState(info fs.FileInfo) lspWatchedPathState {
	return lspWatchedPathState{mode: info.Mode(), size: info.Size(), modTime: info.ModTime()}
}

func lspPathWithin(name, root string) bool {
	relative, err := filepath.Rel(root, name)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
