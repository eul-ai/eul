package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const maxMessageHistoryEntryBytes = 2*1024*1024 + 1024

type messageHistoryStore struct {
	path string
	now  func() time.Time
}

type messageHistoryEntry struct {
	SessionID string `json:"session_id"`
	Timestamp int64  `json:"ts"`
	Text      string `json:"text"`
}

func newMessageHistoryStore(home string) *messageHistoryStore {
	return &messageHistoryStore{path: filepath.Join(home, "history.jsonl"), now: time.Now}
}

func (store *messageHistoryStore) Load(excludedSessionID string) (entries []string, err error) {
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open message history: %w", err)
	}
	defer func() {
		err = errors.Join(err, releaseMessageHistoryFile(file))
	}()

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH); err != nil {
		return nil, fmt.Errorf("lock message history: %w", err)
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, bufio.MaxScanTokenSize), maxMessageHistoryEntryBytes)
	for scanner.Scan() {
		entry, err := decodeMessageHistoryEntry(scanner.Bytes())
		if err != nil {
			return nil, err
		}
		if entry.SessionID != excludedSessionID {
			entries = append(entries, entry.Text)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read message history: %w", err)
	}
	return entries, nil
}

func (store *messageHistoryStore) Append(sessionID, text string) (err error) {
	entry := messageHistoryEntry{SessionID: sessionID, Timestamp: store.now().Unix(), Text: text}
	if err := entry.validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode message history: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxMessageHistoryEntryBytes {
		return errors.New("message history entry is too large")
	}

	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return fmt.Errorf("create message history directory: %w", err)
	}
	file, err := os.OpenFile(store.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open message history: %w", err)
	}
	defer func() {
		err = errors.Join(err, releaseMessageHistoryFile(file))
	}()

	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("secure message history: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock message history: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		return fmt.Errorf("append message history: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync message history: %w", err)
	}
	return nil
}

func (entry messageHistoryEntry) validate() error {
	switch {
	case !validSessionID(entry.SessionID):
		return errors.New("message history entry has an invalid session ID")
	case entry.Timestamp <= 0:
		return errors.New("message history entry has an invalid timestamp")
	case strings.TrimSpace(entry.Text) == "" || !utf8.ValidString(entry.Text) || strings.IndexByte(entry.Text, 0) >= 0:
		return errors.New("message history entry has invalid text")
	}
	return nil
}

func decodeMessageHistoryEntry(encoded []byte) (messageHistoryEntry, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()

	var entry messageHistoryEntry
	if err := decoder.Decode(&entry); err != nil {
		return messageHistoryEntry{}, fmt.Errorf("decode message history: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return messageHistoryEntry{}, errors.New("decode message history: multiple JSON values")
		}
		return messageHistoryEntry{}, fmt.Errorf("decode message history: %w", err)
	}
	if err := entry.validate(); err != nil {
		return messageHistoryEntry{}, err
	}
	return entry, nil
}

func releaseMessageHistoryFile(file *os.File) error {
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock message history: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close message history: %w", closeErr)
	}
	return nil
}
