package oauth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCredentialFileLockIsExclusiveAndCancelable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	first := NewManager(path)
	second := NewManager(path)
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
