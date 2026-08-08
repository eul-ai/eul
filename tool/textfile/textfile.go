package textfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

var ErrChanged = errors.New("text file changed since it was read")

type Snapshot struct {
	RequestedPath string
	Path          string
	Data          []byte
	Mode          os.FileMode
	info          os.FileInfo
}

type Replacement struct {
	snapshot      Snapshot
	temporaryPath string
	committed     bool
}

func Load(path string) (Snapshot, error) {
	targetPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Snapshot{}, err
	}
	file, err := os.Open(targetPath)
	if err != nil {
		return Snapshot{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Snapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return Snapshot{}, fmt.Errorf("%s is not a regular file", filepath.ToSlash(path))
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return Snapshot{}, err
	}
	if err := Validate(data); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		RequestedPath: path,
		Path:          targetPath,
		Data:          data,
		Mode:          info.Mode(),
		info:          info,
	}, nil
}

func Validate(data []byte) error {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return errors.New("binary file is not supported")
	}
	return nil
}

func Prepare(snapshot Snapshot, data []byte) (*Replacement, error) {
	temporary, err := os.CreateTemp(filepath.Dir(snapshot.Path), ".yaah-replace-*")
	if err != nil {
		return nil, err
	}

	replacement := &Replacement{snapshot: snapshot, temporaryPath: temporary.Name()}
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(replacement.temporaryPath)
	}
	if err := temporary.Chmod(snapshot.Mode); err != nil {
		cleanup()
		return nil, err
	}
	if _, err := temporary.Write(data); err != nil {
		cleanup()
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(replacement.temporaryPath)
		return nil, err
	}
	return replacement, nil
}

func (r *Replacement) Verify() error {
	info, err := os.Lstat(r.snapshot.Path)
	if err != nil {
		return err
	}
	if r.snapshot.info == nil || !info.Mode().IsRegular() || !os.SameFile(r.snapshot.info, info) || info.Mode() != r.snapshot.Mode || info.Size() != r.snapshot.info.Size() || !info.ModTime().Equal(r.snapshot.info.ModTime()) {
		return r.changedError()
	}
	current, err := os.ReadFile(r.snapshot.Path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, r.snapshot.Data) {
		return r.changedError()
	}
	return nil
}

func (r *Replacement) changedError() error {
	return fmt.Errorf("%w: %s", ErrChanged, filepath.ToSlash(r.snapshot.RequestedPath))
}

func (r *Replacement) Commit() error {
	if err := r.Verify(); err != nil {
		return err
	}
	if err := os.Rename(r.temporaryPath, r.snapshot.Path); err != nil {
		return err
	}
	r.committed = true
	return nil
}

func (r *Replacement) Discard() {
	if !r.committed {
		_ = os.Remove(r.temporaryPath)
	}
}

func Replace(snapshot Snapshot, data []byte) error {
	replacement, err := Prepare(snapshot, data)
	if err != nil {
		return err
	}
	defer replacement.Discard()
	return replacement.Commit()
}
