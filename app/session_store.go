package app

import (
	"bufio"
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
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

const (
	maxSessionBytes          = int64(512 * 1024 * 1024)
	sessionStateFileName     = "state.json"
	sessionTranscriptAName   = "transcript-a.jsonl"
	sessionTranscriptBName   = "transcript-b.jsonl"
	sessionLockFileName      = "lock"
	initialTranscriptSlot    = "a"
	maxTranscriptRecordBytes = maxSessionBytes
)

var errSessionInUse = errors.New("session is already in use")

type sessionStore struct {
	root string
	now  func() time.Time
}

type sessionHandle struct {
	store      *sessionStore
	path       string
	lock       *os.File
	record     sessionRecord
	transcript terminal.Transcript
	head       sessionTranscriptHead
	warnings   []string
	persisted  bool
	unusable   bool
	closed     bool
}

func newSessionStore(home string) *sessionStore {
	return &sessionStore{root: filepath.Join(home, "sessions"), now: time.Now}
}

func (store *sessionStore) Create(
	provider string,
	cwd string,
	models modelSet,
	thinkingLevel agent.ThinkingLevel,
	agentCheckpoint agent.Checkpoint,
	subagentCheckpoint subagent.Checkpoint,
	terminalCheckpoint terminal.Checkpoint,
	fastMode bool,
) (*sessionHandle, error) {
	if err := models.validate(); err != nil {
		return nil, err
	}

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
	lock, err := acquireSessionLock(sessionLockPath(path))
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}

	now := store.now().UTC()
	record := sessionRecord{
		sessionMetadata: sessionMetadata{
			Version:          sessionStateVersion,
			ID:               id,
			CreatedAt:        now,
			UpdatedAt:        now,
			Status:           sessionIdle,
			Provider:         provider,
			WorkingDirectory: cwd,
			Model:            models.main,
			FastModel:        models.fast,
			BalancedModel:    models.balanced,
			ThinkingLevel:    thinkingLevel,
			FastMode:         fastMode,
			Description:      terminalCheckpoint.Description(),
		},
		Agent:    agentCheckpoint,
		Subagent: subagentCheckpoint,
		Terminal: terminalCheckpoint,
	}
	handle := &sessionHandle{
		store:  store,
		path:   path,
		lock:   lock,
		record: record,
		head:   sessionTranscriptHead{Slot: initialTranscriptSlot},
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
	record, transcript, head, err := readStoredSession(path)
	if err != nil {
		_ = releaseSessionLock(lock)
		return nil, err
	}
	if filepath.Dir(path) != store.workspaceDirectory(record.WorkingDirectory) {
		_ = releaseSessionLock(lock)
		return nil, errors.New("session is stored under the wrong workspace")
	}
	if err := truncateTranscriptTail(path, head); err != nil {
		_ = releaseSessionLock(lock)
		return nil, err
	}

	return &sessionHandle{
		store:      store,
		path:       path,
		lock:       lock,
		record:     record,
		transcript: transcript,
		head:       head,
		warnings:   warnings,
		persisted:  true,
	}, nil
}

func (store *sessionStore) List(cwd string) ([]sessionSummary, []string, error) {
	directory := store.workspaceDirectory(cwd)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list sessions: %w", err)
	}

	var summaries []sessionSummary
	var warnings []string
	for _, entry := range entries {
		if !entry.IsDir() || !validSessionID(entry.Name()) {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		lock, err := acquireSessionLock(sessionLockPath(path))
		if errors.Is(err, errSessionInUse) {
			continue
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Skipped session %s: %v", filepath.ToSlash(path), err))
			continue
		}

		statePath := sessionStatePath(path)
		summary, workingDirectory, err := readSessionSummary(statePath)
		err = errors.Join(err, releaseSessionLock(lock))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Skipped session %s: %v", filepath.ToSlash(path), err))
			continue
		}
		if workingDirectory != cwd {
			warnings = append(warnings, fmt.Sprintf("Skipped session %s: stored working directory does not match", filepath.ToSlash(path)))
			continue
		}
		summaries = append(summaries, summary)
	}
	slices.SortFunc(summaries, func(left, right sessionSummary) int {
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
	return filepath.Join(store.workspaceDirectory(cwd), summaries[0].ID), warnings, nil
}

func (store *sessionStore) findSessionPath(cwd, id string) (string, error) {
	if !validSessionID(id) {
		return "", errors.New("invalid session ID")
	}

	local := filepath.Join(store.workspaceDirectory(cwd), id)
	if info, err := os.Lstat(local); err == nil && info.IsDir() {
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
		candidate := filepath.Join(store.root, workspace.Name(), id)
		info, err := os.Lstat(candidate)
		switch {
		case err == nil && info.IsDir():
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
	next.UpdatedAt = handle.store.now().UTC()
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
	transcript, terminalState, err := terminal.SplitCheckpoint(record.Terminal)
	if err != nil {
		return err
	}

	head := handle.head
	delta, changed := terminal.DiffTranscript(handle.transcript, transcript)
	if changed {
		encodedDelta, err := json.Marshal(delta)
		if err != nil {
			return fmt.Errorf("encode transcript delta: %w", err)
		}
		encodedDelta = append(encodedDelta, '\n')
		if int64(len(encodedDelta)) > maxTranscriptRecordBytes {
			return fmt.Errorf("session transcript record exceeds %d bytes", maxTranscriptRecordBytes)
		}
		if err := appendTranscriptRecord(handle.path, head, encodedDelta); err != nil {
			return err
		}
		head.Bytes += int64(len(encodedDelta))
		head.BlockCount = transcript.BlockCount()
		if !handle.persisted || handle.head.Bytes == 0 {
			head.BaseBytes = int64(len(encodedDelta))
			head.DeltaBytes = 0
		} else {
			head.DeltaBytes += int64(len(encodedDelta))
		}
	}

	state := sessionState{
		sessionMetadata: record.sessionMetadata,
		Agent:           record.Agent,
		Subagent:        record.Subagent,
		Terminal:        terminalState,
		Transcript:      head,
	}
	encodedState, err := encodeSessionState(state)
	if err != nil {
		return err
	}
	if int64(len(encodedState))+head.Bytes > maxSessionBytes {
		return fmt.Errorf("session exceeds %d bytes", maxSessionBytes)
	}
	ambiguous, err := writeSessionState(sessionStatePath(handle.path), encodedState)
	if err != nil {
		if ambiguous {
			handle.unusable = true
		}
		return err
	}

	handle.record = record
	handle.transcript = transcript
	handle.head = head
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
	return nil
}

func sessionStatePath(path string) string {
	return filepath.Join(path, sessionStateFileName)
}

func sessionTranscriptPath(path, slot string) string {
	name := sessionTranscriptAName
	if slot == "b" {
		name = sessionTranscriptBName
	}
	return filepath.Join(path, name)
}

func sessionLockPath(path string) string {
	return filepath.Join(path, sessionLockFileName)
}

func readSessionSummary(path string) (sessionSummary, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return sessionSummary{}, "", fmt.Errorf("inspect session state: %w", err)
	}
	if !info.Mode().IsRegular() {
		return sessionSummary{}, "", errors.New("session state path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return sessionSummary{}, "", errors.New("session state file permissions must be 0600")
	}
	if info.Size() > maxSessionBytes {
		return sessionSummary{}, "", fmt.Errorf("session exceeds %d bytes", maxSessionBytes)
	}

	file, err := os.Open(path)
	if err != nil {
		return sessionSummary{}, "", fmt.Errorf("open session state: %w", err)
	}
	defer file.Close()

	return decodeSessionSummary(io.LimitReader(file, maxSessionBytes+1))
}

func readSessionRecord(path string) (sessionRecord, error) {
	record, _, _, err := readStoredSession(path)
	return record, err
}

func readStoredSession(path string) (sessionRecord, terminal.Transcript, sessionTranscriptHead, error) {
	state, err := readSessionState(sessionStatePath(path))
	if err != nil {
		return sessionRecord{}, terminal.Transcript{}, sessionTranscriptHead{}, err
	}
	transcript, err := readTranscript(path, state.Transcript)
	if err != nil {
		return sessionRecord{}, terminal.Transcript{}, sessionTranscriptHead{}, err
	}
	terminalCheckpoint, err := terminal.JoinCheckpoint(transcript, state.Terminal)
	if err != nil {
		return sessionRecord{}, terminal.Transcript{}, sessionTranscriptHead{}, err
	}
	record := sessionRecord{
		sessionMetadata: state.sessionMetadata,
		Agent:           state.Agent,
		Subagent:        state.Subagent,
		Terminal:        terminalCheckpoint,
	}
	if err := validateSessionRecord(record); err != nil {
		return sessionRecord{}, terminal.Transcript{}, sessionTranscriptHead{}, err
	}
	return record, transcript, state.Transcript, nil
}

func readSessionState(path string) (sessionState, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return sessionState{}, fmt.Errorf("inspect session state: %w", err)
	}
	if !info.Mode().IsRegular() {
		return sessionState{}, errors.New("session state path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return sessionState{}, errors.New("session state file permissions must be 0600")
	}
	if info.Size() > maxSessionBytes {
		return sessionState{}, fmt.Errorf("session exceeds %d bytes", maxSessionBytes)
	}

	encoded, err := os.ReadFile(path)
	if err != nil {
		return sessionState{}, fmt.Errorf("read session state: %w", err)
	}
	state, err := decodeSessionState(encoded)
	if err != nil {
		return sessionState{}, err
	}
	if int64(len(encoded))+state.Transcript.Bytes > maxSessionBytes {
		return sessionState{}, fmt.Errorf("session exceeds %d bytes", maxSessionBytes)
	}
	return state, nil
}

func readTranscript(path string, head sessionTranscriptHead) (terminal.Transcript, error) {
	if head.Bytes == 0 {
		return terminal.EmptyTranscript(), nil
	}

	transcriptPath := sessionTranscriptPath(path, head.Slot)
	info, err := os.Lstat(transcriptPath)
	if err != nil {
		return terminal.Transcript{}, fmt.Errorf("inspect session transcript: %w", err)
	}
	if !info.Mode().IsRegular() {
		return terminal.Transcript{}, errors.New("session transcript path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return terminal.Transcript{}, errors.New("session transcript file permissions must be 0600")
	}
	if info.Size() < head.Bytes {
		return terminal.Transcript{}, errors.New("session transcript is shorter than its committed state")
	}

	file, err := os.Open(transcriptPath)
	if err != nil {
		return terminal.Transcript{}, fmt.Errorf("open session transcript: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(io.LimitReader(file, head.Bytes))
	transcript := terminal.EmptyTranscript()
	var consumed int64
	for consumed < head.Bytes {
		line, err := reader.ReadBytes('\n')
		consumed += int64(len(line))
		if err != nil {
			return terminal.Transcript{}, fmt.Errorf("read session transcript: %w", err)
		}
		if int64(len(line)) > maxTranscriptRecordBytes {
			return terminal.Transcript{}, fmt.Errorf("session transcript record exceeds %d bytes", maxTranscriptRecordBytes)
		}
		if len(line) == 1 {
			return terminal.Transcript{}, errors.New("session transcript contains an empty record")
		}

		var delta terminal.TranscriptDelta
		if err := json.Unmarshal(line[:len(line)-1], &delta); err != nil {
			return terminal.Transcript{}, fmt.Errorf("decode session transcript: %w", err)
		}
		transcript, err = terminal.ApplyTranscriptDelta(transcript, delta)
		if err != nil {
			return terminal.Transcript{}, err
		}
	}
	if consumed != head.Bytes {
		return terminal.Transcript{}, errors.New("session transcript offset is inconsistent")
	}
	if transcript.BlockCount() != head.BlockCount {
		return terminal.Transcript{}, errors.New("session transcript block count is inconsistent")
	}
	return transcript, nil
}

func appendTranscriptRecord(path string, head sessionTranscriptHead, encoded []byte) (err error) {
	transcriptPath := sessionTranscriptPath(path, head.Slot)
	created := false
	if info, inspectErr := os.Lstat(transcriptPath); errors.Is(inspectErr, os.ErrNotExist) {
		created = true
	} else if inspectErr != nil {
		return fmt.Errorf("inspect session transcript: %w", inspectErr)
	} else if !info.Mode().IsRegular() {
		return errors.New("session transcript path is not a regular file")
	}

	file, err := os.OpenFile(transcriptPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open session transcript: %w", err)
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("secure session transcript: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect session transcript: %w", err)
	}
	if info.Size() < head.Bytes {
		return errors.New("session transcript is shorter than its committed state")
	}
	if info.Size() > head.Bytes {
		if err := file.Truncate(head.Bytes); err != nil {
			return fmt.Errorf("truncate session transcript: %w", err)
		}
	}
	if _, err := file.Seek(head.Bytes, io.SeekStart); err != nil {
		return fmt.Errorf("seek session transcript: %w", err)
	}
	if err := writeAll(file, encoded); err != nil {
		return fmt.Errorf("append session transcript: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync session transcript: %w", err)
	}
	if created {
		if err := syncDirectory(path); err != nil {
			return err
		}
	}
	return nil
}

func truncateTranscriptTail(path string, head sessionTranscriptHead) error {
	transcriptPath := sessionTranscriptPath(path, head.Slot)
	info, err := os.Lstat(transcriptPath)
	if errors.Is(err, os.ErrNotExist) && head.Bytes == 0 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect session transcript: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("session transcript path is not a regular file")
	}
	if info.Size() <= head.Bytes {
		return nil
	}
	file, err := os.OpenFile(transcriptPath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open session transcript: %w", err)
	}
	if err := file.Truncate(head.Bytes); err != nil {
		_ = file.Close()
		return fmt.Errorf("truncate session transcript: %w", err)
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

func writeSessionState(path string, encoded []byte) (bool, error) {
	directory := filepath.Dir(path)
	if err := secureSessionDirectory(directory); err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(directory, ".state-*")
	if err != nil {
		return false, fmt.Errorf("create session state temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return false, fmt.Errorf("secure session state temporary file: %w", err)
	}
	if err := writeAll(temporary, encoded); err != nil {
		cleanup()
		return false, fmt.Errorf("write session state temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return false, fmt.Errorf("sync session state temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return false, fmt.Errorf("close session state temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return false, fmt.Errorf("replace session state file: %w", err)
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
