package agent

import (
	"encoding/json"
	"sync"
)

type toolEventTracker struct {
	mu        sync.Mutex
	tools     Toolbox
	sink      EventSink
	streamed  map[string]streamedTool
	order     []string
	updateErr error
}

type streamedTool struct {
	call                  ToolCall
	presentation          ToolPresentation
	presentationFinalized bool
}

type trackedToolUpdateSink struct {
	tracker *toolEventTracker
	call    ToolCall
}

func (sink *trackedToolUpdateSink) Update(presentation ToolPresentation) error {
	return sink.tracker.publishUpdate(sink.call, presentation)
}

func (sink *trackedToolUpdateSink) SetFinal(presentation ToolPresentation) {
	sink.tracker.setFinal(sink.call, presentation)
}

func newToolEventTracker(tools Toolbox, sink EventSink) *toolEventTracker {
	return &toolEventTracker{
		tools:    tools,
		sink:     sink,
		streamed: make(map[string]streamedTool),
	}
}

func (tracker *toolEventTracker) emitProvider(event Event) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return emit(tracker.sink, event)
}

func (tracker *toolEventTracker) observeSnapshot(snapshot ToolCallSnapshot) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	if snapshot.ID == "" || snapshot.Name == "" {
		return nil
	}
	presentation := clonePresentation(tracker.tools.Presentation(snapshot))
	call := ToolCall{ID: snapshot.ID, Name: snapshot.Name, Arguments: []byte(snapshot.RawArguments)}
	current, exists := tracker.streamed[snapshot.ID]
	if exists && presentationsEqual(current.presentation, presentation) {
		current.call = call
		tracker.streamed[snapshot.ID] = current
		return nil
	}

	kind := EventToolUpdate
	if !exists {
		kind = EventToolStart
		tracker.order = append(tracker.order, snapshot.ID)
	}
	if err := emit(tracker.sink, Event{Kind: kind, Call: call, Presentation: presentation}); err != nil {
		return err
	}
	tracker.streamed[snapshot.ID] = streamedTool{call: call, presentation: presentation}
	return nil
}

func (tracker *toolEventTracker) reconcileFinal(calls []ToolCall) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	final := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		final[call.ID] = struct{}{}
	}
	for _, callID := range tracker.order {
		streamed, exists := tracker.streamed[callID]
		if !exists {
			continue
		}
		if _, exists := final[callID]; exists {
			continue
		}
		result := ToolResult{CallID: callID, Tool: streamed.call.Name, Output: "tool call did not complete", IsError: true}
		if err := emit(tracker.sink, Event{Kind: EventToolEnd, Call: streamed.call, Presentation: streamed.presentation, Result: result}); err != nil {
			return err
		}
		delete(tracker.streamed, callID)
	}
	return nil
}

func (tracker *toolEventTracker) beginExecution(call ToolCall) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	presentation := clonePresentation(tracker.tools.Presentation(completeToolCallSnapshot(call)))
	streamed, exists := tracker.streamed[call.ID]
	if !exists {
		if err := emit(tracker.sink, Event{Kind: EventToolStart, Call: call, Presentation: presentation}); err != nil {
			return err
		}
		streamed = streamedTool{call: call, presentation: presentation}
		tracker.order = append(tracker.order, call.ID)
	} else if !presentationsEqual(streamed.presentation, presentation) {
		if err := emit(tracker.sink, Event{Kind: EventToolUpdate, Call: call, Presentation: presentation}); err != nil {
			return err
		}
		streamed.presentation = presentation
	}
	streamed.call = call
	tracker.streamed[call.ID] = streamed
	return emit(tracker.sink, Event{Kind: EventToolExecute, Call: call, Presentation: streamed.presentation})
}

func (tracker *toolEventTracker) update(call ToolCall) ToolUpdateSink {
	return &trackedToolUpdateSink{tracker: tracker, call: call}
}

func (tracker *toolEventTracker) publishUpdate(call ToolCall, next ToolPresentation) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	if tracker.updateErr != nil {
		return tracker.updateErr
	}
	streamed := tracker.streamed[call.ID]
	if streamed.presentationFinalized {
		return nil
	}
	next = clonePresentation(next)
	if presentationsEqual(streamed.presentation, next) {
		return nil
	}
	if err := emit(tracker.sink, Event{Kind: EventToolUpdate, Call: call, Presentation: next}); err != nil {
		tracker.updateErr = err
		return err
	}
	streamed.presentation = next
	tracker.streamed[call.ID] = streamed
	return nil
}

func (tracker *toolEventTracker) setFinal(call ToolCall, final ToolPresentation) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	streamed := tracker.streamed[call.ID]
	streamed.presentation = clonePresentation(final)
	streamed.presentationFinalized = true
	tracker.streamed[call.ID] = streamed
}

func (tracker *toolEventTracker) updateError() error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.updateErr
}

func (tracker *toolEventTracker) end(call ToolCall, result ToolResult) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	streamed := tracker.streamed[call.ID]
	delete(tracker.streamed, call.ID)
	return emit(tracker.sink, Event{Kind: EventToolEnd, Call: call, Presentation: streamed.presentation, Result: result})
}

func (tracker *toolEventTracker) closeRemaining(cause error) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	for _, callID := range tracker.order {
		streamed, exists := tracker.streamed[callID]
		if !exists {
			continue
		}
		result := ToolResult{
			CallID:  streamed.call.ID,
			Tool:    streamed.call.Name,
			Output:  cause.Error(),
			IsError: true,
		}
		if err := emit(tracker.sink, Event{Kind: EventToolEnd, Call: streamed.call, Presentation: streamed.presentation, Result: result}); err != nil {
			return err
		}
		delete(tracker.streamed, callID)
	}
	return nil
}

func completeToolCallSnapshot(call ToolCall) ToolCallSnapshot {
	arguments := make(map[string]any)
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil || arguments == nil {
		arguments = make(map[string]any)
	}
	return ToolCallSnapshot{
		ID:           call.ID,
		Name:         call.Name,
		RawArguments: string(call.Arguments),
		Arguments:    arguments,
		Complete:     true,
	}
}

func clonePresentation(presentation ToolPresentation) ToolPresentation {
	presentation.Lines = append([]string(nil), presentation.Lines...)
	presentation.Diff = append([]ToolDiffLine(nil), presentation.Diff...)
	return presentation
}

func presentationsEqual(left, right ToolPresentation) bool {
	if left.Title != right.Title || left.Arguments != right.Arguments || left.Markdown != right.Markdown || left.Outcome != right.Outcome || len(left.Lines) != len(right.Lines) || len(left.Diff) != len(right.Diff) {
		return false
	}
	for index := range left.Lines {
		if left.Lines[index] != right.Lines[index] {
			return false
		}
	}
	for index := range left.Diff {
		if left.Diff[index] != right.Diff[index] {
			return false
		}
	}
	return true
}
