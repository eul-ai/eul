package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/skill"
	"github.com/eul-ai/eul/subagent"
)

type testProvider struct {
	generate func(context.Context, agent.Request) (agent.Response, error)
	compact  func(context.Context, agent.Request) (agent.CompactResponse, error)
}

func (provider testProvider) Generate(ctx context.Context, request agent.Request, _ agent.StreamObserver) (agent.Response, error) {
	return provider.generate(ctx, request)
}

func (testProvider) RetryGeneration(error, int) (time.Duration, bool) { return 0, false }

func (testProvider) ShouldCompact(agent.Request, agent.Usage) bool { return false }

func (provider testProvider) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	return provider.compact(ctx, request)
}

func (testProvider) ShouldCompactAfterError(agent.Request, error) bool { return false }

func textContent(text string) []agent.ContentPart {
	return []agent.ContentPart{{Kind: agent.ContentPartText, Text: text}}
}

func createTestHandle(t *testing.T) *Handle {
	t.Helper()
	handle, err := NewStore(t.TempDir()).Create(Settings{WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func openTestSession(t *testing.T, handle *Handle, provider agent.Provider, warnings ...string) *Session {
	t.Helper()
	engine := agent.New(provider, nil, agent.Options{Checkpoint: handle.Record().Agent})
	subagents := subagent.NewManager(nil, handle.Record().Subagent)
	t.Cleanup(subagents.Close)
	return New(Config{
		Handle:    handle,
		Engine:    engine,
		Subagents: subagents,
		Settings:  agent.NewSettings(agent.ThinkingMedium, false),
		Warnings:  warnings,
	})
}

func waitSettled(t *testing.T, s *Session) error {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case update := <-s.Updates():
			if update.Kind == UpdateSettled {
				return update.Err
			}
		case <-timeout:
			t.Fatal("turn did not settle")
		}
	}
}

func userTexts(s *Session) []string {
	entries, _ := s.Sync(nil)
	var texts []string
	for _, entry := range entries {
		if entry.Kind == EntryUser {
			texts = append(texts, entry.Text)
		}
	}
	return texts
}

func countNotices(entries []Entry) int {
	count := 0
	for _, entry := range entries {
		if entry.Kind == EntryNotice {
			count++
		}
	}
	return count
}

func TestResumingDoesNotAccumulateNotices(t *testing.T) {
	store := NewStore(t.TempDir())
	cwd := t.TempDir()
	handle, err := store.Create(Settings{WorkingDirectory: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.save(state{Transcript: []Entry{{Kind: EntryUser, Text: "prompt"}}}, true, agent.ThinkingMedium, false); err != nil {
		t.Fatal(err)
	}
	id := handle.Record().ID
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	for _, wantNotices := range []int{2, 1, 1} {
		handle, err := store.Open(cwd, id)
		if err != nil {
			t.Fatal(err)
		}
		conversation := openTestSession(t, handle, nil, "config warning")
		entries, _ := conversation.Sync(nil)
		if len(entries) != 1+wantNotices || entries[0].Kind != EntryUser || countNotices(entries) != wantNotices {
			t.Fatalf("entries = %+v, want %d notices", entries, wantNotices)
		}
		if err := conversation.Close(); err != nil {
			t.Fatal(err)
		}
	}

	handle, err = store.Open(cwd, id)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if transcript := handle.Record().Transcript; len(transcript) != 1 || countNotices(transcript) != 0 {
		t.Fatalf("persisted transcript = %+v", transcript)
	}
}

func TestSessionSavesTurnCheckpointsWithLastDraft(t *testing.T) {
	handle := createTestHandle(t)
	conversation := openTestSession(t, handle, nil)
	defer conversation.Close()

	conversation.transcript.AppendUser(textContent("prompt"))
	conversation.SetDraft(Draft{Input: "draft"})
	conversation.queue = [][]agent.ContentPart{
		textContent("queued"),
		{{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("png")}}},
	}
	if err := conversation.sink(agent.Event{Kind: agent.EventAssistantText, Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	if err := conversation.sink(agent.Event{Kind: agent.EventCheckpoint, Checkpoint: &agent.Checkpoint{State: []byte("state")}}); err != nil {
		t.Fatal(err)
	}

	record := handle.Record()
	if !record.Active || string(record.Agent.State) != "state" || record.Draft.Input != "draft" || !slices.Equal(record.Draft.QueuedInputs, []string{"queued"}) {
		t.Fatalf("record = %+v", record)
	}
	if len(record.Transcript) != 2 || record.Transcript[1].Text != "answer" || record.Description != "prompt" {
		t.Fatalf("transcript = %+v, description = %q", record.Transcript, record.Description)
	}
}

func TestSendStartsTurnAfterSavingActiveSession(t *testing.T) {
	handle := createTestHandle(t)
	started := make(chan struct{})
	release := make(chan struct{})
	conversation := openTestSession(t, handle, testProvider{generate: func(context.Context, agent.Request) (agent.Response, error) {
		close(started)
		<-release
		return agent.Response{Text: "done"}, nil
	}})
	defer conversation.Close()

	handle.closed = true
	if err := conversation.Send(textContent("prompt")); err == nil || conversation.Running() || len(userTexts(conversation)) != 0 {
		t.Fatalf("error = %v, running = %v, prompts = %q", err, conversation.Running(), userTexts(conversation))
	}
	handle.closed = false

	if err := conversation.Send(textContent("prompt")); err != nil {
		t.Fatal(err)
	}
	<-started
	if !handle.Record().Active || !conversation.Running() || !slices.Equal(userTexts(conversation), []string{"prompt"}) {
		t.Fatalf("active = %v, running = %v, prompts = %q", handle.Record().Active, conversation.Running(), userTexts(conversation))
	}
	close(release)
	if err := waitSettled(t, conversation); err != nil || handle.Record().Active || conversation.Running() {
		t.Fatalf("error = %v, active = %v, running = %v", err, handle.Record().Active, conversation.Running())
	}
}

func TestPromptsQueuedDuringCompactionRunAsSeparateTurns(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var prompts []string
	conversation := openTestSession(t, createTestHandle(t), testProvider{
		generate: func(_ context.Context, request agent.Request) (agent.Response, error) {
			prompts = append(prompts, request.Inputs[len(request.Inputs)-1].PlainText())
			return agent.Response{Text: "done", State: []byte("state")}, nil
		},
		compact: func(context.Context, agent.Request) (agent.CompactResponse, error) {
			close(started)
			<-release
			return agent.CompactResponse{State: []byte("compacted")}, nil
		},
	})
	defer conversation.Close()

	if err := conversation.Send(textContent("first")); err != nil {
		t.Fatal(err)
	}
	if err := waitSettled(t, conversation); err != nil {
		t.Fatal(err)
	}
	if err := conversation.Compact(); err != nil {
		t.Fatal(err)
	}
	<-started
	for _, prompt := range []string{"second", "third"} {
		if err := conversation.Send(textContent(prompt)); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	if err := waitSettled(t, conversation); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(prompts, []string{"first", "second", "third"}) || !slices.Equal(userTexts(conversation), prompts) || len(conversation.Queued()) != 0 {
		t.Fatalf("prompts = %q, transcript = %q, queued = %d", prompts, userTexts(conversation), len(conversation.Queued()))
	}
}

func TestFailedTurnKeepsQueuedPrompts(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	failure := errors.New("failed")
	conversation := openTestSession(t, createTestHandle(t), testProvider{generate: func(context.Context, agent.Request) (agent.Response, error) {
		close(started)
		<-release
		return agent.Response{}, failure
	}})
	defer conversation.Close()

	if err := conversation.Send(textContent("first")); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := conversation.Send(textContent("retry this")); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := waitSettled(t, conversation); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	if queued := conversation.TakeQueued(); len(queued) != 1 || contentText(queued[0]) != "retry this" || conversation.Running() {
		t.Fatalf("queued = %+v, running = %v", queued, conversation.Running())
	}
}

func TestQueuedPromptReachesModelOrEditorOnce(t *testing.T) {
	takes := map[string]func(*Session) [][]agent.ContentPart{
		"interrupt": (*Session).Interrupt,
		"restore":   (*Session).TakeQueued,
	}
	for name, take := range takes {
		for spin := range 50 {
			started := make(chan struct{})
			release := make(chan struct{})
			var requests int
			conversation := openTestSession(t, createTestHandle(t), testProvider{generate: func(ctx context.Context, _ agent.Request) (agent.Response, error) {
				if requests++; requests == 1 {
					close(started)
					<-release
				}
				return agent.Response{}, ctx.Err()
			}})

			if err := conversation.Send(textContent("first")); err != nil {
				t.Fatal(err)
			}
			<-started
			if err := conversation.Send(textContent("queued")); err != nil {
				t.Fatal(err)
			}
			close(release)
			// A varying delay lands the take before, while and after the
			// engine delivers the queued prompt.
			for range spin {
				runtime.Gosched()
			}
			returned := take(conversation)
			_ = waitSettled(t, conversation)
			returned = append(returned, conversation.TakeQueued()...)

			delivered := slices.Contains(userTexts(conversation), "queued")
			if len(returned) > 1 || delivered == (len(returned) == 1) {
				t.Fatalf("%s: delivered = %v, returned = %d", name, delivered, len(returned))
			}
			if err := conversation.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestExpandSkillWithoutMovingImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: review\ndescription: Review code\n---\nInstructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills, _ := skill.Load(filepath.Dir(filepath.Dir(path)))

	content, err := expandSkill([]agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "  "},
		{Kind: agent.ContentPartText, Text: "/skill:review check tests"},
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("png")}},
		{Kind: agent.ContentPartText, Text: " after"},
	}, skills)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 4 || content[0].Text != "  " || !strings.Contains(content[1].Text, "Instructions") || !strings.HasSuffix(content[1].Text, "</skill>\n\ncheck tests") || content[2].Kind != agent.ContentPartImage || content[3].Text != " after" {
		t.Fatalf("content = %+v", content)
	}
}
