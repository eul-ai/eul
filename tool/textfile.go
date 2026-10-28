package tool

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"unicode/utf8"
)

const maxTextFileBytes = 16 * 1024 * 1024

// fileMutation serializes file changes so concurrent edits within one tool
// round apply in turn instead of overwriting each other.
var fileMutation sync.Mutex

type textSnapshot struct {
	path string
	data []byte
	mode os.FileMode
}

func loadText(path string) (textSnapshot, error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return textSnapshot{}, err
	}
	file, err := os.Open(target)
	if err != nil {
		return textSnapshot{}, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return textSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return textSnapshot{}, fmt.Errorf("%s is not a regular file", filepath.ToSlash(path))
	}
	data, err := io.ReadAll(io.LimitReader(file, maxTextFileBytes+1))
	if err != nil {
		return textSnapshot{}, err
	}
	if err := validateText(data); err != nil {
		return textSnapshot{}, err
	}
	return textSnapshot{path: target, data: data, mode: info.Mode()}, nil
}

func replaceText(snapshot textSnapshot, data []byte) error {
	if err := validateText(data); err != nil {
		return err
	}

	temporary, err := os.CreateTemp(filepath.Dir(snapshot.path), ".eul-replace-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())

	if err := temporary.Chmod(snapshot.mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), snapshot.path)
}

func validateText(data []byte) error {
	if len(data) > maxTextFileBytes {
		return fmt.Errorf("text file exceeds %d bytes", maxTextFileBytes)
	}
	if isBinary(data) {
		return errors.New("binary file is not supported")
	}
	return nil
}

func isBinary(data []byte) bool {
	return !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0
}
