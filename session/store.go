package session

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
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/terminal"
)

const (
	sessionRecordVersion       = 1
	maxSessionBytes            = int64(64 * 1024 * 1024)
	maxSessionDescriptionBytes = 120
)

type sessionStatus string

const (
	sessionIdle   sessionStatus = "idle"
	sessionActive sessionStatus = "active"
)

type sessionRecord struct {
	Version          int                 `json:"version"`
	ID               string              `json:"id"`
	Revision         uint64              `json:"revision"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
	Status           sessionStatus       `json:"status"`
	Provider         string              `json:"provider"`
	WorkingDirectory string              `json:"working_directory"`
	Model            string              `json:"model"`
	FastModel        string              `json:"fast_model,omitempty"`
	BalancedModel    string              `json:"balanced_model,omitempty"`
	ThinkingLevel    agent.ThinkingLevel `json:"thinking_level"`
	Description      string              `json:"description,omitempty"`
	Agent            agent.Checkpoint    `json:"agent"`
	Terminal         terminal.Checkpoint `json:"terminal"`
}

type sessionStore struct {
	root string
	now  func() time.Time
}

type sessionHandle struct {
	store    *sessionStore
	path     string
	lock     *os.File
	record   sessionRecord
	warnings []string
	closed   bool
}

func newSessionStore(home string) *sessionStore {
	return &sessionStore{root: filepath.Join(home, "sessions"), now: time.Now}
}

func (store *sessionStore) Create(
	provider string,
	cwd string,
	model string,
	fastModel string,
	balancedModel string,
	thinkingLevel agent.ThinkingLevel,
	agentCheckpoint agent.Checkpoint,
	terminalCheckpoint terminal.Checkpoint,
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
	path := filepath.Join(workspaceDirectory, id+".json")
	lock, err := acquireSessionLock(sessionLockPath(path))
	if err != nil {
		return nil, err
	}

	now := store.now().UTC()
	handle := &sessionHandle{
		store: store,
		path:  path,
		lock:  lock,
		record: sessionRecord{
			Version:          sessionRecordVersion,
			ID:               id,
			CreatedAt:        now,
			UpdatedAt:        now,
			Status:           sessionIdle,
			Provider:         provider,
			WorkingDirectory: cwd,
			Model:            model,
			FastModel:        fastModel,
			BalancedModel:    balancedModel,
			ThinkingLevel:    thinkingLevel,
			Description:      terminalCheckpoint.Description(),
			Agent:            agentCheckpoint,
			Terminal:         terminalCheckpoint,
		},
	}
	if handle.record.Description != "" {
		record := handle.record
		record.Revision = 1
		if err := handle.write(record); err != nil {
			return nil, errors.Join(err, handle.Close())
		}
		handle.record = record
	}
	return handle, nil
}

func (store *sessionStore) Open(ctx context.Context, cwd, id string) (*sessionHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var path string
	var warnings []string
	var err error
	if id == "" {
		path, warnings, err = store.mostRecentPath(cwd)
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
	if filepath.Dir(path) != store.workspaceDirectory(record.WorkingDirectory) {
		_ = releaseSessionLock(lock)
		return nil, errors.New("session is stored under the wrong workspace")
	}

	return &sessionHandle{store: store, path: path, lock: lock, record: record, warnings: warnings}, nil
}

func (store *sessionStore) List(cwd string) ([]terminal.SessionSummary, []string, error) {
	directory := store.workspaceDirectory(cwd)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list sessions: %w", err)
	}

	var summaries []terminal.SessionSummary
	var warnings []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		record, err := readSessionRecord(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Skipped session %s: %v", filepath.ToSlash(path), err))
			continue
		}
		if record.WorkingDirectory != cwd {
			warnings = append(warnings, fmt.Sprintf("Skipped session %s: stored working directory does not match", filepath.ToSlash(path)))
			continue
		}
		description := record.Description
		if description == "" {
			description = record.Terminal.Description()
		}
		if description == "" {
			continue
		}
		summaries = append(summaries, terminal.SessionSummary{
			ID:          record.ID,
			Description: description,
			UpdatedAt:   record.UpdatedAt,
			Active:      record.Status == sessionActive,
		})
	}
	slices.SortFunc(summaries, func(left, right terminal.SessionSummary) int {
		if order := right.UpdatedAt.Compare(left.UpdatedAt); order != 0 {
			return order
		}
		return strings.Compare(left.ID, right.ID)
	})
	return summaries, warnings, nil
}

func (store *sessionStore) workspaceDirectory(cwd string) string {
	digest := sha256.Sum256([]byte(cwd))
	return filepath.Join(store.root, hex.EncodeToString(digest[:]))
}

func (store *sessionStore) mostRecentPath(cwd string) (string, []string, error) {
	summaries, warnings, err := store.List(cwd)
	if err != nil {
		return "", nil, err
	}
	if len(summaries) == 0 {
		message := fmt.Sprintf("no saved sessions for %s", cwd)
		if len(warnings) > 0 {
			message += ": " + strings.Join(warnings, "; ")
		}
		return "", nil, errors.New(message)
	}
	return filepath.Join(store.workspaceDirectory(cwd), summaries[0].ID+".json"), warnings, nil
}

func (store *sessionStore) findSessionPath(cwd, id string) (string, error) {
	if !validSessionID(id) {
		return "", errors.New("invalid session ID")
	}

	local := filepath.Join(store.workspaceDirectory(cwd), id+".json")
	if info, err := os.Lstat(local); err == nil && info.Mode().IsRegular() {
		return local, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect session: %w", err)
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
		candidate := filepath.Join(store.root, workspace.Name(), id+".json")
		info, err := os.Lstat(candidate)
		switch {
		case err == nil && info.Mode().IsRegular():
			return candidate, nil
		case err == nil:
			continue
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return "", fmt.Errorf("inspect session: %w", err)
		}
	}
	return "", fmt.Errorf("session %s not found", id)
}

func (handle *sessionHandle) Record() sessionRecord {
	return handle.record
}

func (handle *sessionHandle) Save(
	agentCheckpoint agent.Checkpoint,
	terminalCheckpoint terminal.Checkpoint,
	active bool,
	thinkingLevel agent.ThinkingLevel,
) error {
	if handle.closed {
		return errors.New("session is closed")
	}

	next := handle.record
	next.UpdatedAt = handle.store.now().UTC()
	next.Status = sessionIdle
	if active {
		next.Status = sessionActive
	}
	next.ThinkingLevel = thinkingLevel
	next.Agent = agentCheckpoint
	next.Terminal = terminalCheckpoint
	if next.Description == "" {
		next.Description = terminalCheckpoint.Description()
	}
	if next.Description == "" {
		handle.record = next
		return nil
	}
	next.Revision++
	if err := handle.write(next); err != nil {
		return err
	}
	handle.record = next
	return nil
}

func (handle *sessionHandle) write(record sessionRecord) error {
	if err := validateSessionRecord(record); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	encoded = append(encoded, '\n')
	if int64(len(encoded)) > maxSessionBytes {
		return fmt.Errorf("session exceeds %d bytes", maxSessionBytes)
	}
	return writeSessionRecord(handle.path, encoded)
}

func (handle *sessionHandle) Close() error {
	if handle == nil || handle.closed {
		return nil
	}
	handle.closed = true
	if err := releaseSessionLock(handle.lock); err != nil {
		return err
	}
	if handle.record.Revision != 0 {
		return nil
	}
	if err := os.Remove(sessionLockPath(handle.path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove empty session lock: %w", err)
	}
	return nil
}

func sessionLockPath(path string) string {
	return strings.TrimSuffix(path, ".json") + ".lock"
}

func readSessionRecord(path string) (sessionRecord, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return sessionRecord{}, fmt.Errorf("inspect session: %w", err)
	}
	if !info.Mode().IsRegular() {
		return sessionRecord{}, errors.New("session path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return sessionRecord{}, errors.New("session file permissions must be 0600")
	}

	file, err := os.Open(path)
	if err != nil {
		return sessionRecord{}, fmt.Errorf("open session: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxSessionBytes+1))
	if err != nil {
		return sessionRecord{}, fmt.Errorf("read session: %w", err)
	}
	if int64(len(body)) > maxSessionBytes {
		return sessionRecord{}, fmt.Errorf("session exceeds %d bytes", maxSessionBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var record sessionRecord
	if err := decoder.Decode(&record); err != nil {
		return sessionRecord{}, fmt.Errorf("decode session: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return sessionRecord{}, errors.New("decode session: multiple JSON values")
		}
		return sessionRecord{}, fmt.Errorf("decode session: %w", err)
	}
	if err := validateSessionRecord(record); err != nil {
		return sessionRecord{}, err
	}
	return record, nil
}

func validateSessionRecord(record sessionRecord) error {
	switch {
	case record.Version != sessionRecordVersion:
		return fmt.Errorf("unsupported session version %d", record.Version)
	case !validSessionID(record.ID):
		return errors.New("session has an invalid ID")
	case record.Revision == 0:
		return errors.New("session has no revision")
	case record.CreatedAt.IsZero() || record.UpdatedAt.IsZero():
		return errors.New("session timestamps are missing")
	case record.UpdatedAt.Before(record.CreatedAt):
		return errors.New("session timestamps are inconsistent")
	case record.Status != sessionIdle && record.Status != sessionActive:
		return fmt.Errorf("session has invalid status %q", record.Status)
	case !backend.ValidID(record.Provider):
		return fmt.Errorf("session provider %q is invalid", record.Provider)
	case !filepath.IsAbs(record.WorkingDirectory) || filepath.Clean(record.WorkingDirectory) != record.WorkingDirectory:
		return errors.New("session working directory is not canonical")
	case strings.TrimSpace(record.Model) == "":
		return errors.New("session model is empty")
	case record.FastModel != "" && strings.TrimSpace(record.FastModel) == "":
		return errors.New("session fast model is invalid")
	case record.BalancedModel != "" && strings.TrimSpace(record.BalancedModel) == "":
		return errors.New("session balanced model is invalid")
	case !record.ThinkingLevel.Valid():
		return errors.New("session thinking level is invalid")
	case !record.Agent.Initialized() || !record.Terminal.Initialized():
		return errors.New("session checkpoints are missing")
	case !utf8.ValidString(record.Description) || len(record.Description) > maxSessionDescriptionBytes || strings.IndexFunc(record.Description, unicode.IsControl) >= 0:
		return errors.New("session description is invalid")
	}
	return nil
}

func writeSessionRecord(path string, encoded []byte) error {
	directory := filepath.Dir(path)
	if err := secureSessionDirectory(directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".session-*")
	if err != nil {
		return fmt.Errorf("create session temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("secure session temporary file: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		cleanup()
		return fmt.Errorf("write session temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync session temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close session temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace session file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure session file: %w", err)
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open session directory: %w", err)
	}
	syncErr := directoryFile.Sync()
	closeErr := directoryFile.Close()
	if syncErr != nil {
		return fmt.Errorf("sync session directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close session directory: %w", closeErr)
	}
	return nil
}

func secureSessionDirectory(path string) error {
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
			return nil, errors.New("session is already in use")
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
