package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type messageHistoryStore struct {
	path string
}

type messageHistoryEntry struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

func newMessageHistoryStore(home string) *messageHistoryStore {
	return &messageHistoryStore{path: filepath.Join(home, "history.jsonl")}
}

// Load returns the prompts of other sessions followed by the entries of
// sessionID, so the session's own entries are recalled first. Slash commands
// are only recalled in the session that ran them.
func (store *messageHistoryStore) Load(sessionID string) ([]string, error) {
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open message history: %w", err)
	}
	defer file.Close()

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH); err != nil {
		return nil, fmt.Errorf("lock message history: %w", err)
	}

	var others, own []string
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		var entry messageHistoryEntry
		switch {
		case json.Unmarshal(line, &entry) != nil:
		case entry.SessionID == sessionID:
			own = append(own, entry.Text)
		case strings.HasPrefix(strings.TrimSpace(entry.Text), "/"):
		default:
			others = append(others, entry.Text)
		}

		if errors.Is(readErr, io.EOF) {
			return append(others, own...), nil
		}
		if readErr != nil {
			return nil, fmt.Errorf("read message history: %w", readErr)
		}
	}
}

func (store *messageHistoryStore) Append(sessionID, text string) (err error) {
	encoded, err := json.Marshal(messageHistoryEntry{SessionID: sessionID, Text: text})
	if err != nil {
		return fmt.Errorf("encode message history: %w", err)
	}
	encoded = append(encoded, '\n')

	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return fmt.Errorf("create message history directory: %w", err)
	}
	file, err := os.OpenFile(store.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open message history: %w", err)
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()

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
