package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type inboxProvider func(context.Context, Request, StreamObserver) (Response, error)

func (provider inboxProvider) Generate(ctx context.Context, request Request, observer StreamObserver) (Response, error) {
	return provider(ctx, request, observer)
}

type fakeInbox struct {
	mu           sync.Mutex
	messages     []InboxBatch
	acknowledged [][]uint64
	active       string
	settle       func() bool
}

func (inbox *fakeInbox) SnapshotInbox() InboxBatch {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	if len(inbox.messages) == 0 {
		return InboxBatch{}
	}
	batch := inbox.messages[0]
	batch.MessageIDs = append([]uint64(nil), batch.MessageIDs...)
	return batch
}

func (inbox *fakeInbox) AcknowledgeInbox(batch InboxBatch) error {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	inbox.acknowledged = append(inbox.acknowledged, append([]uint64(nil), batch.MessageIDs...))
	if len(inbox.messages) > 0 {
		inbox.messages = inbox.messages[1:]
	}
	return nil
}

func (inbox *fakeInbox) InboxEmpty() bool {
	if inbox.settle != nil {
		return inbox.settle()
	}
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	return len(inbox.messages) == 0
}

func (inbox *fakeInbox) decorate(request *Request) {
	if inbox.active != "" {
		request.Instructions = strings.TrimSpace(request.Instructions) + "\n\n" + inbox.active
	}
}

func TestEngineDeliversAndAcknowledgesInbox(t *testing.T) {
	inbox := &fakeInbox{
		messages: []InboxBatch{{MessageIDs: []uint64{7}, Text: "<subagent_notifications>result</subagent_notifications>"}},
		active:   "Active subagents:\n- subagent-2: inspect (running)",
	}
	provider := inboxProvider(func(_ context.Context, request Request, _ StreamObserver) (Response, error) {
		if strings.Count(request.Instructions, "subagent-2: inspect") != 1 {
			t.Fatalf("instructions = %q", request.Instructions)
		}
		if len(request.Inputs) != 2 || request.Inputs[1].Kind != InputInbox || !strings.Contains(request.Inputs[1].PlainText(), "result") {
			t.Fatalf("inputs = %+v", request.Inputs)
		}
		return Response{Text: "answer", State: []byte("state")}, nil
	})
	engine := New(provider, &fakeToolbox{}, Options{Model: "model", Inbox: inbox, DecorateRequest: inbox.decorate})

	result, err := engine.Run(context.Background(), "start", discardEvents)
	if err != nil || result.Text != "answer" {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	if len(inbox.acknowledged) != 1 || len(inbox.acknowledged[0]) != 1 || inbox.acknowledged[0][0] != 7 {
		t.Fatalf("acknowledgements = %v", inbox.acknowledged)
	}
}

func TestEngineAcknowledgesInboxOnlyAfterCheckpointSucceeds(t *testing.T) {
	inbox := &fakeInbox{messages: []InboxBatch{{MessageIDs: []uint64{8}, Text: "notification"}}}
	provider := inboxProvider(func(context.Context, Request, StreamObserver) (Response, error) {
		return Response{Text: "answer", State: []byte("delivered")}, nil
	})
	engine := New(provider, &fakeToolbox{}, Options{Model: "model", Inbox: inbox, DecorateRequest: inbox.decorate, Checkpointing: true})
	persistErr := errors.New("persist failed")

	_, err := engine.Run(context.Background(), "start", func(event Event) error {
		if event.Kind == EventCheckpoint {
			return persistErr
		}
		return nil
	})
	if !errors.Is(err, persistErr) || len(inbox.acknowledged) != 0 || len(inbox.messages) != 1 {
		t.Fatalf("error = %v, acknowledgements = %v, messages = %v", err, inbox.acknowledged, inbox.messages)
	}
}

func TestEngineLeavesInboxPendingAfterGenerationFailure(t *testing.T) {
	inbox := &fakeInbox{messages: []InboxBatch{{MessageIDs: []uint64{1}, Text: "notification"}}}
	provider := inboxProvider(func(context.Context, Request, StreamObserver) (Response, error) {
		return Response{}, errors.New("failed")
	})
	engine := New(provider, &fakeToolbox{}, Options{Model: "model", Inbox: inbox, DecorateRequest: inbox.decorate})

	if _, err := engine.Run(context.Background(), "start", discardEvents); err == nil {
		t.Fatal("generation succeeded")
	}
	if len(inbox.acknowledged) != 0 || len(inbox.messages) != 1 {
		t.Fatalf("acknowledgements = %v, messages = %v", inbox.acknowledged, inbox.messages)
	}
}

func TestEngineAutomaticallyCompactsOrdinaryStateBeforeDeliveringInbox(t *testing.T) {
	inbox := &fakeInbox{
		messages: []InboxBatch{{MessageIDs: []uint64{3}, Text: "completion"}},
		active:   "Active subagents:\n- subagent-4: inspect (running)",
	}
	generateCalls := 0
	provider := &compactingProvider{
		Provider: inboxProvider(func(_ context.Context, request Request, _ StreamObserver) (Response, error) {
			generateCalls++
			if string(request.State) != "compacted" || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputInbox || request.Inputs[0].PlainText() != "completion" {
				t.Fatalf("generation request = %+v", request)
			}
			if strings.Count(request.Instructions, "subagent-4: inspect") != 1 {
				t.Fatalf("active context count in %q", request.Instructions)
			}
			return Response{Text: "synthesized", State: []byte("delivered")}, nil
		}),
		shouldCompact: func(request Request, _ Usage) bool {
			if len(request.Inputs) != 2 || request.Inputs[0].Kind != InputUser || request.Inputs[1].Kind != InputInbox {
				t.Fatalf("sizing request = %+v", request)
			}
			if strings.Count(request.Instructions, "subagent-4: inspect") != 1 {
				t.Fatalf("sizing active context count in %q", request.Instructions)
			}
			return true
		},
		compact: func(_ context.Context, request Request) (CompactResponse, error) {
			if len(request.Inputs) != 1 || request.Inputs[0].Kind != InputUser || request.Inputs[0].PlainText() != "start" {
				t.Fatalf("compaction request included inbox: %+v", request)
			}
			if strings.Contains(request.Instructions, "subagent-4: inspect") {
				t.Fatalf("compaction request included dynamic context: %q", request.Instructions)
			}
			return CompactResponse{State: []byte("compacted")}, nil
		},
	}
	engine := New(provider, &fakeToolbox{}, Options{Model: "model", Inbox: inbox, DecorateRequest: inbox.decorate})
	engine.conversation.state = []byte("full")

	result, err := engine.Run(context.Background(), "start", discardEvents)
	if err != nil || result.Text != "synthesized" || generateCalls != 1 || len(inbox.acknowledged) != 1 {
		t.Fatalf("result = %+v, error = %v, generate calls = %d, acknowledgements = %v", result, err, generateCalls, inbox.acknowledged)
	}
}

func TestEngineReattachesInboxAfterErrorCompaction(t *testing.T) {
	contextLimit := errors.New("context limit exceeded")
	inbox := &fakeInbox{
		messages: []InboxBatch{{MessageIDs: []uint64{5}, Text: "completion"}},
		active:   "Active subagents:\n- subagent-6: inspect (running)",
	}
	generateCalls := 0
	provider := &compactingProvider{
		Provider: inboxProvider(func(_ context.Context, request Request, _ StreamObserver) (Response, error) {
			generateCalls++
			if strings.Count(request.Instructions, "subagent-6: inspect") != 1 {
				t.Fatalf("active context count in %q", request.Instructions)
			}
			if generateCalls == 1 {
				if len(request.Inputs) != 2 || request.Inputs[1].Kind != InputInbox || request.Inputs[1].PlainText() != "completion" {
					t.Fatalf("initial generation request = %+v", request)
				}
				return Response{}, contextLimit
			}
			if string(request.State) != "compacted" || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputInbox || request.Inputs[0].PlainText() != "completion" {
				t.Fatalf("retry request = %+v", request)
			}
			return Response{Text: "synthesized", State: []byte("delivered")}, nil
		}),
		shouldCompactAfterError: func(request Request, err error) bool {
			if !errors.Is(err, contextLimit) || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputUser {
				t.Fatalf("error policy request = %+v, error = %v", request, err)
			}
			if strings.Contains(request.Instructions, "subagent-6: inspect") {
				t.Fatalf("error policy request included dynamic context: %q", request.Instructions)
			}
			return true
		},
		compact: func(_ context.Context, request Request) (CompactResponse, error) {
			if len(request.Inputs) != 1 || request.Inputs[0].Kind != InputUser {
				t.Fatalf("compaction request included inbox: %+v", request)
			}
			return CompactResponse{State: []byte("compacted")}, nil
		},
	}
	engine := New(provider, &fakeToolbox{}, Options{Model: "model", Inbox: inbox, DecorateRequest: inbox.decorate})
	engine.conversation.state = []byte("full")

	result, err := engine.Run(context.Background(), "start", discardEvents)
	if err != nil || result.Text != "synthesized" || generateCalls != 2 || len(inbox.acknowledged) != 1 {
		t.Fatalf("result = %+v, error = %v, generate calls = %d, acknowledgements = %v", result, err, generateCalls, inbox.acknowledged)
	}
}

func TestEngineCompletionAfterSettlementRemainsForNextUserTurn(t *testing.T) {
	inbox := &fakeInbox{}
	calls := 0
	inbox.settle = func() bool {
		inbox.mu.Lock()
		defer inbox.mu.Unlock()
		if len(inbox.messages) != 0 {
			return false
		}
		go func() {
			inbox.mu.Lock()
			inbox.messages = append(inbox.messages, InboxBatch{MessageIDs: []uint64{4}, Text: "after settlement"})
			inbox.mu.Unlock()
		}()
		inbox.settle = func() bool {
			inbox.mu.Lock()
			defer inbox.mu.Unlock()
			return len(inbox.messages) == 0
		}
		return true
	}
	provider := inboxProvider(func(_ context.Context, request Request, _ StreamObserver) (Response, error) {
		calls++
		if calls == 2 {
			if len(request.Inputs) != 2 || request.Inputs[0].Kind != InputUser || request.Inputs[1].Kind != InputInbox || request.Inputs[1].PlainText() != "after settlement" {
				t.Fatalf("second turn inputs = %+v", request.Inputs)
			}
		}
		return Response{Text: "answer", State: []byte("state")}, nil
	})
	engine := New(provider, &fakeToolbox{}, Options{Model: "model", Inbox: inbox, DecorateRequest: inbox.decorate})

	if _, err := engine.Run(context.Background(), "first", discardEvents); err != nil {
		t.Fatal(err)
	}
	for {
		inbox.mu.Lock()
		pending := len(inbox.messages) > 0
		inbox.mu.Unlock()
		if pending {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if calls != 1 || len(inbox.acknowledged) != 0 {
		t.Fatalf("calls = %d, acknowledgements = %v", calls, inbox.acknowledged)
	}
	if _, err := engine.Run(context.Background(), "second", discardEvents); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(inbox.acknowledged) != 1 {
		t.Fatalf("calls = %d, acknowledgements = %v", calls, inbox.acknowledged)
	}
}

func TestEngineSettlementDeliversCompletionThatRacesFinalAnswer(t *testing.T) {
	inbox := &fakeInbox{}
	calls := 0
	inbox.settle = func() bool {
		inbox.mu.Lock()
		defer inbox.mu.Unlock()
		if len(inbox.messages) == 0 && calls == 1 {
			inbox.messages = append(inbox.messages, InboxBatch{MessageIDs: []uint64{2}, Text: "late completion"})
			return false
		}
		return len(inbox.messages) == 0
	}
	provider := inboxProvider(func(_ context.Context, request Request, _ StreamObserver) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{Text: "premature", State: []byte("first")}, nil
		case 2:
			if len(request.Inputs) != 1 || request.Inputs[0].Kind != InputInbox || request.Inputs[0].PlainText() != "late completion" {
				t.Fatalf("second inputs = %+v", request.Inputs)
			}
			return Response{Text: "final", State: []byte("second")}, nil
		default:
			t.Fatalf("unexpected call %d", calls)
			return Response{}, nil
		}
	})
	engine := New(provider, &fakeToolbox{}, Options{Model: "model", Inbox: inbox, DecorateRequest: inbox.decorate})

	result, err := engine.Run(context.Background(), "start", discardEvents)
	if err != nil || result.Text != "final" || calls != 2 {
		t.Fatalf("result = %+v, error = %v, calls = %d", result, err, calls)
	}
}
