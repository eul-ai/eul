package filesearch

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

type fileCandidate struct {
	path      string
	name      string
	directory bool
	hidden    bool
}

type discoverFilesFunc func(context.Context, fileSearchSpec, func([]fileCandidate) error) (bool, error)

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
