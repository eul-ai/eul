package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type credentialStore struct {
	path string
}

func (store *credentialStore) writeLocked(ctx context.Context, credential credentials) error {
	return store.withLock(ctx, func() error { return store.write(credential) })
}

func (store *credentialStore) read() (credentials, error) {
	body, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return credentials{}, errors.New("oauth: not logged in; run 'eul login'")
	}
	if err != nil {
		return credentials{}, fmt.Errorf("oauth: read credential file: %w", err)
	}

	var credential credentials
	if err := json.Unmarshal(body, &credential); err != nil || credential.ExpiresAt <= 0 || credential.AccessToken == "" || credential.RefreshToken == "" || credential.AccountID == "" {
		return credentials{}, errors.New("oauth: invalid credential file")
	}
	return credential, nil
}

func (store *credentialStore) write(credential credentials) error {
	encoded, err := json.MarshalIndent(credential, "", "  ")
	if err != nil {
		return errors.New("oauth: encode credentials")
	}
	encoded = append(encoded, '\n')

	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("oauth: create credential directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".auth-*")
	if err != nil {
		return fmt.Errorf("oauth: create credential temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return fmt.Errorf("oauth: write credential temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("oauth: sync credential temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("oauth: close credential temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return fmt.Errorf("oauth: replace credential file: %w", err)
	}
	return nil
}

func (store *credentialStore) remove() error {
	if err := os.Remove(store.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("oauth: remove credential file: %w", err)
	}
	return nil
}

func (store *credentialStore) withLock(ctx context.Context, function func() error) error {
	lock, err := store.acquireLock(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()

	result := function()
	unlockErr := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if result != nil {
		return result
	}
	if unlockErr != nil {
		return fmt.Errorf("oauth: release credential lock: %w", unlockErr)
	}
	return nil
}

func (store *credentialStore) acquireLock(ctx context.Context) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	lockPath := store.path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("oauth: create lock directory: %w", err)
	}
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("oauth: open credential lock: %w", err)
	}

	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			lock.Close()
			return nil, fmt.Errorf("oauth: acquire credential lock: %w", err)
		}
		if err := sleepContext(ctx, 50*time.Millisecond); err != nil {
			lock.Close()
			return nil, err
		}
	}
}
