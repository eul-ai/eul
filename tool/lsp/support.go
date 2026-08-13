package lsp

import (
	"errors"
	"path/filepath"
)

type workspace struct {
	cwd string
}

func newWorkspace(cwd string) workspace {
	return workspace{cwd: cwd}
}

func (w workspace) resolve(name string) (string, error) {
	if name == "" {
		return "", errors.New("path is required")
	}
	if filepath.IsAbs(name) {
		return filepath.Clean(name), nil
	}
	return filepath.Join(w.cwd, name), nil
}
