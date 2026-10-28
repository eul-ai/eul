package session

import (
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
)

func TestHistoryLoadsOwnSessionLast(t *testing.T) {
	const (
		firstSession  = "11111111111111111111111111111111"
		secondSession = "22222222222222222222222222222222"
	)

	history := NewHistory(t.TempDir())
	for _, entry := range []struct {
		sessionID string
		text      string
	}{
		{sessionID: firstSession, text: "first prompt"},
		{sessionID: secondSession, text: "second prompt"},
		{sessionID: secondSession, text: "/compact"},
		{sessionID: firstSession, text: "third prompt"},
		{sessionID: firstSession, text: "/goal ship it"},
	} {
		if err := history.Append(entry.sessionID, entry.text); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := history.Load(firstSession)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(entries, []string{"second prompt", "first prompt", "third prompt", "/goal ship it"}) {
		t.Fatalf("entries = %q", entries)
	}

	info, err := os.Stat(history.path)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("permissions = %o, want 600", permissions)
	}
}

func TestHistorySkipsUndecodableLines(t *testing.T) {
	history := NewHistory(t.TempDir())
	body := "not json\n" + `{"session_id":"other","text":"kept"}` + "\n{\"text\":"
	if err := os.WriteFile(history.path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := history.Load("session")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(entries, []string{"kept"}) {
		t.Fatalf("entries = %q", entries)
	}
}

func TestHistorySerializesConcurrentAppends(t *testing.T) {
	history := NewHistory(t.TempDir())

	const count = 16
	var wait sync.WaitGroup
	errors := make(chan error, count)
	for index := range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			sessionID := fmt.Sprintf("%032x", index+1)
			errors <- history.Append(sessionID, fmt.Sprintf("prompt %02d", index))
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}

	entries, err := history.Load("")
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
