package app

import (
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestMessageHistoryStoreLoadsOtherSessions(t *testing.T) {
	const (
		firstSession  = "11111111111111111111111111111111"
		secondSession = "22222222222222222222222222222222"
	)

	store := newMessageHistoryStore(t.TempDir())
	store.now = func() time.Time { return time.Unix(123, 0) }
	for _, entry := range []struct {
		sessionID string
		text      string
	}{
		{sessionID: firstSession, text: "first prompt"},
		{sessionID: secondSession, text: "second prompt"},
		{sessionID: firstSession, text: "third prompt"},
	} {
		if err := store.Append(entry.sessionID, entry.text); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := store.Load(firstSession)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(entries, []string{"second prompt"}) {
		t.Fatalf("entries = %q", entries)
	}

	info, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("permissions = %o, want 600", permissions)
	}
}

func TestMessageHistoryStoreSerializesConcurrentAppends(t *testing.T) {
	store := newMessageHistoryStore(t.TempDir())
	store.now = func() time.Time { return time.Unix(123, 0) }

	const count = 16
	var wait sync.WaitGroup
	errors := make(chan error, count)
	for index := range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			sessionID := fmt.Sprintf("%032x", index+1)
			errors <- store.Append(sessionID, fmt.Sprintf("prompt %02d", index))
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}

	entries, err := store.Load("")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(entries)
	want := make([]string, count)
	for index := range count {
		want[index] = fmt.Sprintf("prompt %02d", index)
	}
	if !slices.Equal(entries, want) {
		t.Fatalf("entries = %q", entries)
	}
}
