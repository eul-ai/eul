package oauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCredentialFileLockIsExclusiveAndCancelable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	first := NewManager(path, Options{})
	second := NewManager(path, Options{})
	held := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.store.withLock(context.Background(), func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := second.store.withLock(ctx, func() error { return errors.New("must not enter") }); !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("contending lock error = %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	entered := false
	if err := second.store.withLock(context.Background(), func() error { entered = true; return nil }); err != nil || !entered {
		t.Fatalf("lock after release entered=%v error=%v", entered, err)
	}
}

func TestInvalidCredentialStorage(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.Symlink("target", credentialPath); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentials(credentialPath, credentials{Version: 1, Type: "oauth"}); err == nil {
		t.Fatal("symlink credential path accepted for writing")
	}
	if _, err := readCredentials(credentialPath); err == nil {
		t.Fatal("symlink credential path accepted for reading")
	}

	insecurePath := filepath.Join(t.TempDir(), "auth.json")
	credential := credentials{Version: 1, Type: "oauth", AccessToken: testJWT(t, "private-account", "permissions"), RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), AccountID: "private-account"}
	if err := writeCredentials(insecurePath, credential); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(insecurePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentials(insecurePath); err == nil {
		t.Fatalf("insecure credential read error = %v", err)
	}
}
