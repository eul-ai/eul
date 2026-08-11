package agent

import "sync"

type toolEventTracker struct {
	mu            sync.Mutex
	tools         Toolbox
	sink          EventSink
	streamed      map[string]streamedTool
	order         []string
	observerEvent bool
	sinkErr       error
	updateErrs    map[string]error
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
		tools:      tools,
		sink:       sink,
		streamed:   make(map[string]streamedTool),
		updateErrs: make(map[string]error),
	}
}

func (tracker *toolEventTracker) emitProvider(event Event) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	tracker.observerEvent = true
	return tracker.emitLocked(event)
}

func (tracker *toolEventTracker) sawObserverEvent() bool {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.observerEvent
}

func (tracker *toolEventTracker) emitLocked(event Event) error {
	if tracker.sinkErr != nil {
		return tracker.sinkErr
	}
	if err := emit(tracker.sink, event); err != nil {
		tracker.sinkErr = err
		return err
	}
	return nil
}

func (tracker *toolEventTracker) observeSnapshot(snapshot ToolCallSnapshot) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	if snapshot.ID == "" || snapshot.Name == "" {
		return nil
	}
	presentation := tracker.tools.Presentation(snapshot).Clone()
	call := ToolCall{ID: snapshot.ID, Name: snapshot.Name, Arguments: []byte(snapshot.RawArguments)}
	current, exists := tracker.streamed[snapshot.ID]
	if exists && current.presentation.Equal(presentation) {
		current.call = call
		tracker.streamed[snapshot.ID] = current
		return nil
	}

	kind := EventToolUpdate
	if !exists {
		kind = EventToolStart
		tracker.order = append(tracker.order, snapshot.ID)
	}
	tracker.observerEvent = true
	if err := tracker.emitLocked(Event{Kind: kind, Call: call, Presentation: presentation}); err != nil {
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
		if err := tracker.emitLocked(Event{Kind: EventToolEnd, Call: streamed.call, Presentation: streamed.presentation, Result: result}); err != nil {
			return err
		}
		delete(tracker.streamed, callID)
	}
	return nil
}

func (tracker *toolEventTracker) beginExecution(call ToolCall) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	presentation := tracker.tools.Presentation(completeToolCallSnapshot(call)).Clone()
	streamed, exists := tracker.streamed[call.ID]
	if !exists {
		if err := tracker.emitLocked(Event{Kind: EventToolStart, Call: call, Presentation: presentation}); err != nil {
			return err
		}
		streamed = streamedTool{call: call, presentation: presentation}
		tracker.order = append(tracker.order, call.ID)
	} else if !streamed.presentation.Equal(presentation) {
		if err := tracker.emitLocked(Event{Kind: EventToolUpdate, Call: call, Presentation: presentation}); err != nil {
			return err
		}
		streamed.presentation = presentation
	}
	streamed.call = call
	tracker.streamed[call.ID] = streamed
	return tracker.emitLocked(Event{Kind: EventToolExecute, Call: call, Presentation: streamed.presentation})
}

func (tracker *toolEventTracker) update(call ToolCall) ToolUpdateSink {
	return &trackedToolUpdateSink{tracker: tracker, call: call}
}

func (tracker *toolEventTracker) publishUpdate(call ToolCall, next ToolPresentation) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	if tracker.sinkErr != nil {
		tracker.updateErrs[call.ID] = tracker.sinkErr
		return tracker.sinkErr
	}
	streamed := tracker.streamed[call.ID]
	if streamed.presentationFinalized {
		return nil
	}
	next = next.Clone()
	if streamed.presentation.Equal(next) {
		return nil
	}
	if err := tracker.emitLocked(Event{Kind: EventToolUpdate, Call: call, Presentation: next}); err != nil {
		tracker.updateErrs[call.ID] = err
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
	streamed.presentation = final.Clone()
	streamed.presentationFinalized = true
	tracker.streamed[call.ID] = streamed
}

func (tracker *toolEventTracker) updateError(call ToolCall) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.updateErrs[call.ID]
}

func (tracker *toolEventTracker) end(call ToolCall, result ToolResult) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	streamed := tracker.streamed[call.ID]
	delete(tracker.streamed, call.ID)
	delete(tracker.updateErrs, call.ID)
	return tracker.emitLocked(Event{Kind: EventToolEnd, Call: call, Presentation: streamed.presentation, Result: result})
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
		if err := tracker.emitLocked(Event{Kind: EventToolEnd, Call: streamed.call, Presentation: streamed.presentation, Result: result}); err != nil {
			return err
		}
		delete(tracker.streamed, callID)
	}
	return nil
}

func completeToolCallSnapshot(call ToolCall) ToolCallSnapshot {
	return ToolCallSnapshot{
		ID:           call.ID,
		Name:         call.Name,
		RawArguments: string(call.Arguments),
		Complete:     true,
	}
}
