package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

const (
	maxSessionBytes         = int64(512 * 1024 * 1024)
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
	unusable  bool
	closed    bool
}

func newSessionStore(home string) *sessionStore {
	return &sessionStore{root: filepath.Join(home, "sessions")}
}

func (store *sessionStore) Create(
	provider string,
	cwd string,
	models backend.Models,
	thinkingLevel agent.ThinkingLevel,
	agentCheckpoint agent.Checkpoint,
	subagentCheckpoint subagent.Checkpoint,
	terminalCheckpoint terminal.Checkpoint,
	fastMode bool,
) (*sessionHandle, error) {
	workspaceDirectory := store.workspaceDirectory(cwd)
	if err := secureSessionDirectory(store.root); err != nil {
		return nil, err
	}
	if err := secureSessionDirectory(workspaceDirectory); err != nil {
		return nil, err
	}

	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(workspaceDirectory, id)
	if err := os.Mkdir(path, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	if err := syncDirectory(workspaceDirectory); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	lock, err := acquireSessionLock(sessionLockPath(path))
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}

	now := time.Now().UTC()
	record := sessionRecord{
		sessionMetadata: sessionMetadata{
			ID:               id,
			CreatedAt:        now,
			UpdatedAt:        now,
			Status:           sessionIdle,
			Provider:         provider,
			WorkingDirectory: cwd,
			Models:           models,
			ThinkingLevel:    thinkingLevel,
			FastMode:         fastMode,
			Description:      terminalCheckpoint.Description(),
		},
		sessionState: sessionState{
			Agent:    agentCheckpoint,
			Subagent: subagentCheckpoint,
			Terminal: terminalCheckpoint,
		},
	}
	handle := &sessionHandle{
		store:  store,
		path:   path,
		lock:   lock,
		record: record,
	}
	if record.Description != "" {
		if err := handle.commit(record); err != nil {
			return nil, errors.Join(err, handle.Close())
		}
	}
	return handle, nil
}

func (store *sessionStore) Open(ctx context.Context, cwd, id string) (*sessionHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var path string
	var err error
	if id == "" {
		path, err = store.mostRecentPath(cwd)
	} else {
		path, err = store.findSessionPath(cwd, id)
	}
	if err != nil {
		return nil, err
	}

	lock, err := acquireSessionLock(sessionLockPath(path))
	if err != nil {
		return nil, err
	}
	record, err := readSessionRecord(path)
	if err != nil {
		_ = releaseSessionLock(lock)
		return nil, err
	}
	if filepath.Base(path) != record.ID {
		_ = releaseSessionLock(lock)
		return nil, errors.New("session is stored under the wrong ID")
	}
	if filepath.Dir(path) != store.workspaceDirectory(record.WorkingDirectory) {
		_ = releaseSessionLock(lock)
		return nil, errors.New("session is stored under the wrong workspace")
	}

	return &sessionHandle{
		store:     store,
		path:      path,
		lock:      lock,
		record:    record,
		persisted: true,
	}, nil
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
		if !entry.IsDir() || !validSessionID(entry.Name()) {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		lock, err := acquireSessionLock(sessionLockPath(path))
		if err != nil {
			continue
		}

		var metadata sessionMetadata
		err = errors.Join(readSessionFile(filepath.Join(path, sessionMetadataFileName), &metadata), releaseSessionLock(lock))
		if err != nil || metadata.ID != entry.Name() || metadata.WorkingDirectory != cwd {
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

func (store *sessionStore) mostRecentPath(cwd string) (string, error) {
	summaries, err := store.List(cwd)
	if err != nil {
		return "", err
	}
	if len(summaries) == 0 {
		return "", fmt.Errorf("no saved sessions for %s", cwd)
	}
	return filepath.Join(store.workspaceDirectory(cwd), summaries[0].ID), nil
}

func (store *sessionStore) findSessionPath(cwd, id string) (string, error) {
	if !validSessionID(id) {
		return "", errors.New("invalid session ID")
	}

	local := filepath.Join(store.workspaceDirectory(cwd), id)
	found, err := storedSessionDirectory(local)
	if err != nil {
		return "", err
	}
	if found {
		return local, nil
	}

	workspaces, err := os.ReadDir(store.root)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("session %s not found", id)
	}
	if err != nil {
		return "", fmt.Errorf("list session workspaces: %w", err)
	}
	for _, workspace := range workspaces {
		if !workspace.IsDir() {
			continue
		}
		candidate := filepath.Join(store.root, workspace.Name(), id)
		found, err := storedSessionDirectory(candidate)
		if err != nil {
			return "", err
		}
		if found {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("session %s not found", id)
}

func storedSessionDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect session: %w", err)
	}
	if !info.IsDir() {
		return false, nil
	}
	if _, err := os.Lstat(filepath.Join(path, sessionMetadataFileName)); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("inspect session state: %w", err)
	}
	return true, nil
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
	if handle.unusable {
		return errors.New("session persistence is unavailable")
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
	if err := validateSessionRecord(record); err != nil {
		return err
	}
	state, err := json.Marshal(record.sessionState)
	if err != nil {
		return fmt.Errorf("encode session state: %w", err)
	}
	if int64(len(state)) > maxSessionBytes {
		return fmt.Errorf("session exceeds %d bytes", maxSessionBytes)
	}
	metadata, err := json.Marshal(record.sessionMetadata)
	if err != nil {
		return fmt.Errorf("encode session metadata: %w", err)
	}

	for _, file := range []struct {
		name    string
		encoded []byte
	}{{sessionStateFileName, state}, {sessionMetadataFileName, metadata}} {
		ambiguous, err := writeSessionFile(filepath.Join(handle.path, file.name), file.encoded)
		if err != nil {
			if ambiguous {
				handle.unusable = true
			}
			return err
		}
	}

	handle.record = record
	handle.persisted = true
	return nil
}

func (handle *sessionHandle) Close() error {
	if handle == nil || handle.closed {
		return nil
	}
	handle.closed = true
	if err := releaseSessionLock(handle.lock); err != nil {
		return err
	}
	if handle.persisted {
		return nil
	}
	if err := os.RemoveAll(handle.path); err != nil {
		return fmt.Errorf("remove empty session: %w", err)
	}
	return syncDirectory(filepath.Dir(handle.path))
}

func sessionLockPath(path string) string {
	return filepath.Join(path, sessionLockFileName)
}

func readSessionRecord(path string) (sessionRecord, error) {
	var record sessionRecord
	if err := readSessionFile(filepath.Join(path, sessionMetadataFileName), &record.sessionMetadata); err != nil {
		return sessionRecord{}, err
	}
	if err := readSessionFile(filepath.Join(path, sessionStateFileName), &record.sessionState); err != nil {
		return sessionRecord{}, err
	}
	if err := validateSessionRecord(record); err != nil {
		return sessionRecord{}, err
	}
	return record, nil
}

func readSessionFile(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect session file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("session file path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("session file permissions must be 0600")
	}
	if info.Size() > maxSessionBytes {
		return fmt.Errorf("session exceeds %d bytes", maxSessionBytes)
	}

	encoded, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read session file: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode %s: multiple JSON values", filepath.Base(path))
		}
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

func writeSessionFile(path string, encoded []byte) (bool, error) {
	directory := filepath.Dir(path)
	if err := secureSessionDirectory(directory); err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(directory, ".session-*")
	if err != nil {
		return false, fmt.Errorf("create session temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return false, fmt.Errorf("secure session temporary file: %w", err)
	}
	if err := writeAll(temporary, encoded); err != nil {
		cleanup()
		return false, fmt.Errorf("write session temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return false, fmt.Errorf("sync session temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return false, fmt.Errorf("close session temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return false, fmt.Errorf("replace session file: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return true, err
	}
	return true, nil
}

func writeAll(writer io.Writer, body []byte) error {
	for len(body) > 0 {
		written, err := writer.Write(body)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		body = body[written:]
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open session directory: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync session directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close session directory: %w", closeErr)
	}
	return nil
}

func secureSessionDirectory(path string) error {
	_, inspectErr := os.Lstat(path)
	created := errors.Is(inspectErr, os.ErrNotExist)
	if inspectErr != nil && !created {
		return fmt.Errorf("inspect session directory: %w", inspectErr)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect session directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("session directory path is not a directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure session directory: %w", err)
	}
	if created {
		return syncDirectory(filepath.Dir(path))
	}
	return nil
}

func acquireSessionLock(path string) (*os.File, error) {
	if err := secureSessionDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("session lock path is not a regular file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect session lock: %w", err)
	}
	lock, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open session lock: %w", err)
	}
	if err := lock.Chmod(0o600); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("secure session lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errSessionInUse
		}
		return nil, fmt.Errorf("lock session: %w", err)
	}
	return lock, nil
}

func releaseSessionLock(lock *os.File) error {
	if lock == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	closeErr := lock.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock session: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close session lock: %w", closeErr)
	}
	return nil
}

func newSessionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create session ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func validSessionID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}
