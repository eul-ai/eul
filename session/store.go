package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/eul-ai/eul/agent"
)

const (
	metadataFileName = "meta.json"
	stateFileName    = "state.json"
	lockFileName     = "lock"
)

var errInUse = errors.New("session is already in use")

type Store struct {
	root string
}

type Handle struct {
	path      string
	lock      *os.File
	record    Record
	persisted bool
	closed    bool
}

func NewStore(home string) *Store {
	return &Store{root: filepath.Join(home, "sessions")}
}

func (store *Store) Create(settings Settings) (*Handle, error) {
	var random [16]byte
	_, _ = rand.Read(random[:])
	id := hex.EncodeToString(random[:])
	path := filepath.Join(store.workspaceDirectory(settings.WorkingDirectory), id)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	lock, err := acquireLock(path)
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}

	now := time.Now().UTC()
	record := Record{metadata: metadata{
		ID:        id,
		CreatedAt: now,
		UpdatedAt: now,
		Settings:  settings,
	}}
	return &Handle{path: path, lock: lock, record: record}, nil
}

func (store *Store) Open(cwd, id string) (*Handle, error) {
	path, err := store.sessionPath(cwd, id)
	if err != nil {
		return nil, err
	}

	lock, err := acquireLock(path)
	if err != nil {
		return nil, err
	}
	record, err := readRecord(path)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	return &Handle{path: path, lock: lock, record: record, persisted: true}, nil
}

func (store *Store) List(cwd string) ([]Summary, error) {
	directory := store.workspaceDirectory(cwd)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	var summaries []Summary
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		lock, err := acquireLock(path)
		if err != nil {
			continue
		}
		var stored metadata
		err = readFile(filepath.Join(path, metadataFileName), &stored)
		_ = lock.Close()
		if err != nil {
			continue
		}
		summaries = append(summaries, Summary{
			ID:          stored.ID,
			Description: stored.Description,
			UpdatedAt:   stored.UpdatedAt,
			Active:      stored.Active,
		})
	}
	slices.SortFunc(summaries, func(left, right Summary) int {
		if order := right.UpdatedAt.Compare(left.UpdatedAt); order != 0 {
			return order
		}
		return strings.Compare(left.ID, right.ID)
	})
	return summaries, nil
}

func (store *Store) workspaceDirectory(cwd string) string {
	digest := sha256.Sum256([]byte(cwd))
	return filepath.Join(store.root, hex.EncodeToString(digest[:]))
}

func (store *Store) sessionPath(cwd, id string) (string, error) {
	if id != "" {
		return store.findSessionPath(id)
	}

	summaries, err := store.List(cwd)
	if err != nil {
		return "", err
	}
	if len(summaries) == 0 {
		return "", fmt.Errorf("no saved sessions for %s", cwd)
	}
	return filepath.Join(store.workspaceDirectory(cwd), summaries[0].ID), nil
}

func (store *Store) findSessionPath(id string) (string, error) {
	workspaces, err := os.ReadDir(store.root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("list session workspaces: %w", err)
	}
	for _, workspace := range workspaces {
		path := filepath.Join(store.root, workspace.Name(), id)
		if _, err := os.Stat(filepath.Join(path, metadataFileName)); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("session %s not found", id)
}

func (handle *Handle) Record() Record {
	return handle.record
}

func (handle *Handle) save(next state, active bool, thinkingLevel agent.ThinkingLevel, fastMode bool) error {
	if handle.closed {
		return errors.New("session is closed")
	}

	record := handle.record
	record.UpdatedAt = time.Now().UTC()
	record.Active = active
	record.ThinkingLevel = thinkingLevel
	record.FastMode = fastMode
	record.state = next
	if record.Description == "" {
		record.Description = description(next.Transcript)
	}
	handle.record = record
	if record.Description == "" {
		return nil
	}

	encodedState, err := json.Marshal(record.state)
	if err != nil {
		return fmt.Errorf("encode session state: %w", err)
	}
	encodedMetadata, err := json.Marshal(record.metadata)
	if err != nil {
		return fmt.Errorf("encode session metadata: %w", err)
	}
	if err := writeFile(filepath.Join(handle.path, stateFileName), encodedState); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(handle.path, metadataFileName), encodedMetadata); err != nil {
		return err
	}
	handle.persisted = true
	return nil
}

func (handle *Handle) Close() error {
	if handle.closed {
		return nil
	}
	handle.closed = true
	if err := handle.lock.Close(); err != nil {
		return fmt.Errorf("unlock session: %w", err)
	}
	if handle.persisted {
		return nil
	}
	if err := os.RemoveAll(handle.path); err != nil {
		return fmt.Errorf("remove empty session: %w", err)
	}
	return nil
}

func readRecord(path string) (Record, error) {
	var record Record
	if err := readFile(filepath.Join(path, metadataFileName), &record.metadata); err != nil {
		return Record{}, err
	}
	if err := readFile(filepath.Join(path, stateFileName), &record.state); err != nil {
		return Record{}, err
	}
	return record, nil
}

func readFile(path string, value any) error {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	if err := json.Unmarshal(encoded, value); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

func writeFile(path string, encoded []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".session-*")
	if err != nil {
		return fmt.Errorf("create session file: %w", err)
	}
	_, err = temporary.Write(encoded)
	err = errors.Join(err, temporary.Sync(), temporary.Close())
	if err == nil {
		err = os.Rename(temporary.Name(), path)
	}
	if err != nil {
		_ = os.Remove(temporary.Name())
		return fmt.Errorf("write session file: %w", err)
	}
	return nil
}

func acquireLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(filepath.Join(path, lockFileName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open session lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errInUse
		}
		return nil, fmt.Errorf("lock session: %w", err)
	}
	return lock, nil
}
