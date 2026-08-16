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

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type credentialStore struct {
	path  string
	sleep func(context.Context, time.Duration) error
}

func (store *credentialStore) writeLocked(ctx context.Context, credential credentials) error {
	return store.withLock(ctx, func() error { return store.write(credential) })
}

func (store *credentialStore) read() (credentials, error) {
	info, err := os.Lstat(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return credentials{}, errors.New("oauth: not logged in; run 'eul login'")
	}
	if err != nil {
		return credentials{}, fmt.Errorf("oauth: inspect credential file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return credentials{}, errors.New("oauth: credential path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return credentials{}, errors.New("oauth: credential file permissions must be 0600")
	}

	file, err := os.Open(store.path)
	if err != nil {
		return credentials{}, fmt.Errorf("oauth: open credential file: %w", err)
	}
	defer file.Close()

	body, truncated, err := backendhttp.ReadBounded(file, maxCredentialBytes)
	if err != nil {
		return credentials{}, fmt.Errorf("oauth: read credential file: %w", err)
	}
	if truncated {
		return credentials{}, errors.New("oauth: credential file is too large")
	}

	var credential credentials
	if err := json.Unmarshal(body, &credential); err != nil || credential.Version != credentialVersion || credential.Type != "oauth" || credential.ExpiresAt <= 0 || credential.AccessToken == "" || credential.RefreshToken == "" || credential.AccountID == "" {
		return credentials{}, errors.New("oauth: invalid credential file")
	}

	return credential, nil
}

func (store *credentialStore) write(credential credentials) error {
	directory := filepath.Dir(store.path)
	if err := secureDirectory(directory, "credential directory"); err != nil {
		return err
	}

	info, err := os.Lstat(store.path)
	if err == nil && !info.Mode().IsRegular() {
		return errors.New("oauth: credential path is not a regular file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("oauth: inspect credential path: %w", err)
	}

	encoded, err := json.MarshalIndent(credential, "", "  ")
	if err != nil {
		return errors.New("oauth: encode credentials")
	}
	encoded = append(encoded, '\n')
	return store.replace(directory, encoded)
}

func (store *credentialStore) replace(directory string, encoded []byte) error {
	temporary, err := os.CreateTemp(directory, ".auth-*")
	if err != nil {
		return fmt.Errorf("oauth: create credential temporary file: %w", err)
	}

	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("oauth: secure credential temporary file: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		return fmt.Errorf("oauth: write credential temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("oauth: sync credential temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("oauth: close credential temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return fmt.Errorf("oauth: replace credential file: %w", err)
	}
	committed = true

	if err := os.Chmod(store.path, 0o600); err != nil {
		return fmt.Errorf("oauth: secure credential file: %w", err)
	}
	return nil
}

func (store *credentialStore) remove() error {
	info, err := os.Lstat(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("oauth: inspect credential file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("oauth: credential path is not a regular file")
	}

	if err := os.Remove(store.path); err != nil {
		return fmt.Errorf("oauth: remove credential file: %w", err)
	}
	return nil
}

func secureDirectory(path, name string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("oauth: create %s: %w", name, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("oauth: secure %s: %w", name, err)
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
	if err := secureDirectory(filepath.Dir(lockPath), "lock directory"); err != nil {
		return nil, err
	}

	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("oauth: open credential lock: %w", err)
	}
	closeOnError := func(err error) (*os.File, error) {
		_ = lock.Close()
		return nil, err
	}

	if err := lock.Chmod(0o600); err != nil {
		return closeOnError(fmt.Errorf("oauth: secure credential lock: %w", err))
	}
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return closeOnError(fmt.Errorf("oauth: acquire credential lock: %w", err))
		}
		if err := store.sleep(ctx, 50*time.Millisecond); err != nil {
			return closeOnError(err)
		}
	}

	if err := ctx.Err(); err != nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		return closeOnError(err)
	}
	return lock, nil
}
