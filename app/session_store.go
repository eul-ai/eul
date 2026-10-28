package app

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
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

const (
	sessionMetadataFileName = "meta.json"
	sessionStateFileName    = "state.json"
	sessionLockFileName     = "lock"
)

var errSessionInUse = errors.New("session is already in use")

type sessionStore struct {
	root string
}

type sessionHandle struct {
	store     *sessionStore
	path      string
	lock      *os.File
	record    sessionRecord
	persisted bool
	closed    bool
}

func newSessionStore(home string) *sessionStore {
	return &sessionStore{root: filepath.Join(home, "sessions")}
}

func (store *sessionStore) Create(settings sessionSettings) (*sessionHandle, error) {
	var random [16]byte
	_, _ = rand.Read(random[:])
	id := hex.EncodeToString(random[:])
	path := filepath.Join(store.workspaceDirectory(settings.cwd), id)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	lock, err := acquireSessionLock(path)
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}

	now := time.Now().UTC()
	record := sessionRecord{sessionMetadata: sessionMetadata{
		ID:               id,
		CreatedAt:        now,
		UpdatedAt:        now,
		Status:           sessionIdle,
		Provider:         settings.provider,
		WorkingDirectory: settings.cwd,
		Models:           settings.models,
		ThinkingLevel:    settings.thinkingLevel,
		FastMode:         settings.fastMode,
	}}
	return &sessionHandle{store: store, path: path, lock: lock, record: record}, nil
}

func (store *sessionStore) Open(cwd, id string) (*sessionHandle, error) {
	path, err := store.sessionPath(cwd, id)
	if err != nil {
		return nil, err
	}

	lock, err := acquireSessionLock(path)
	if err != nil {
		return nil, err
	}
	record, err := readSessionRecord(path)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	return &sessionHandle{store: store, path: path, lock: lock, record: record, persisted: true}, nil
}

func (store *sessionStore) List(cwd string) ([]terminal.SessionSummary, error) {
	directory := store.workspaceDirectory(cwd)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	var summaries []terminal.SessionSummary
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		lock, err := acquireSessionLock(path)
		if err != nil {
			continue
		}
		var metadata sessionMetadata
		err = readSessionFile(filepath.Join(path, sessionMetadataFileName), &metadata)
		_ = lock.Close()
		if err != nil {
			continue
		}
		summaries = append(summaries, metadata.summary())
	}
	slices.SortFunc(summaries, func(left, right terminal.SessionSummary) int {
		if order := right.UpdatedAt.Compare(left.UpdatedAt); order != 0 {
			return order
		}
		return strings.Compare(left.ID, right.ID)
	})
	return summaries, nil
}

func (store *sessionStore) workspaceDirectory(cwd string) string {
	digest := sha256.Sum256([]byte(cwd))
	return filepath.Join(store.root, hex.EncodeToString(digest[:]))
}

func (store *sessionStore) sessionPath(cwd, id string) (string, error) {
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

func (store *sessionStore) findSessionPath(id string) (string, error) {
	workspaces, err := os.ReadDir(store.root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("list session workspaces: %w", err)
	}
	for _, workspace := range workspaces {
		path := filepath.Join(store.root, workspace.Name(), id)
		if _, err := os.Stat(filepath.Join(path, sessionMetadataFileName)); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("session %s not found", id)
}

func (handle *sessionHandle) Record() sessionRecord {
	return handle.record
}

func (handle *sessionHandle) Save(
	agentCheckpoint agent.Checkpoint,
	subagentCheckpoint subagent.Checkpoint,
	terminalCheckpoint terminal.Checkpoint,
	active bool,
	thinkingLevel agent.ThinkingLevel,
	fastMode bool,
) error {
	if handle.closed {
		return errors.New("session is closed")
	}

	next := handle.record
	next.UpdatedAt = time.Now().UTC()
	next.Status = sessionIdle
	if active {
		next.Status = sessionActive
	}
	next.ThinkingLevel = thinkingLevel
	next.FastMode = fastMode
	next.Agent = agentCheckpoint
	next.Subagent = subagentCheckpoint
	next.Terminal = terminalCheckpoint
	if next.Description == "" {
		next.Description = terminalCheckpoint.Description()
	}
	if next.Description == "" {
		handle.record = next
		return nil
	}
	return handle.commit(next)
}

func (handle *sessionHandle) commit(record sessionRecord) error {
	state, err := json.Marshal(record.sessionState)
	if err != nil {
		return fmt.Errorf("encode session state: %w", err)
	}
	metadata, err := json.Marshal(record.sessionMetadata)
	if err != nil {
		return fmt.Errorf("encode session metadata: %w", err)
	}

	if err := writeSessionFile(filepath.Join(handle.path, sessionStateFileName), state); err != nil {
		return err
	}
	if err := writeSessionFile(filepath.Join(handle.path, sessionMetadataFileName), metadata); err != nil {
		return err
	}
	handle.record = record
	handle.persisted = true
	return nil
}

func (handle *sessionHandle) Close() error {
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

func readSessionRecord(path string) (sessionRecord, error) {
	var record sessionRecord
	if err := readSessionFile(filepath.Join(path, sessionMetadataFileName), &record.sessionMetadata); err != nil {
		return sessionRecord{}, err
	}
	if err := readSessionFile(filepath.Join(path, sessionStateFileName), &record.sessionState); err != nil {
		return sessionRecord{}, err
	}
	return record, nil
}

func readSessionFile(path string, value any) error {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	if err := json.Unmarshal(encoded, value); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

func writeSessionFile(path string, encoded []byte) error {
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

func acquireSessionLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(filepath.Join(path, sessionLockFileName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open session lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errSessionInUse
		}
		return nil, fmt.Errorf("lock session: %w", err)
	}
	return lock, nil
}
