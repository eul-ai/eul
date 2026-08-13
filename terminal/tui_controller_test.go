package terminal

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
)

func TestTUIControllerRejectsInvalidClipboardImage(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	clipboardImages := make(chan tuiEvent, 1)
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		readClipboardImage: func(context.Context) (agent.Image, error) {
			return agent.Image{MediaType: "image/png", Data: []byte("png")}, nil
		},
		clipboardImages: clipboardImages,
		stopped:         make(chan struct{}),
	}

	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyCtrlV}); err != nil {
		t.Fatal(err)
	}
	event := <-clipboardImages
	if _, err := controller.transition(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(model.input) != 0 || model.activity.kind != activityError {
		t.Fatalf("input = %+v, activity = %+v", model.input, model.activity)
	}
}

func TestTUIControllerPastesClipboardImage(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	clipboardImages := make(chan tuiEvent, 1)
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
		readClipboardImage: func(context.Context) (agent.Image, error) {
			return agent.Image{MediaType: "image/png", Data: validTestPNG(t)}, nil
		},
		clipboardImages: clipboardImages,
	}

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyCtrlV}}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-clipboardImages:
		if _, err := controller.transition(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("clipboard image was not read")
	}
	content := editorContent(model.input)
	if len(content) != 1 || content[0].Image == nil || content[0].Image.MediaType != "image/png" || !slices.Equal(content[0].Image.Data, validTestPNG(t)) {
		t.Fatalf("content = %+v", content)
	}
}

type checkpointingFakeEngine struct {
	*fakeEngine
}

func (*checkpointingFakeEngine) Checkpoint() (agent.Checkpoint, error) {
	return agent.Checkpoint{}, nil
}

func TestTUIControllerSerializesPermissions(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.running = true
	controller := tuiController{model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard}
	first := make(chan bool, 1)
	second := make(chan bool, 1)

	for _, request := range []PermissionRequest{
		{Title: "Network access", Detail: "git push", Response: first},
		{Title: "Network access", Detail: "ssh host", Response: second},
	} {
		if _, err := controller.handlePermission(request); err != nil {
			t.Fatal(err)
		}
	}
	if model.permission.detail != "git push" || model.permission.total != 2 || len(controller.queuedPermissions) != 1 {
		t.Fatalf("permission=%+v queued=%d", model.permission, len(controller.queuedPermissions))
	}

	if _, err := controller.applyAction(context.Background(), tuiAction{kind: tuiActionAllowPermission}); err != nil {
		t.Fatal(err)
	}
	if allowed := <-first; !allowed {
		t.Fatal("first request was denied")
	}
	if model.permission.detail != "ssh host" || model.permission.index != 2 || model.permission.total != 2 {
		t.Fatalf("next permission = %+v", model.permission)
	}

	if _, err := controller.applyAction(context.Background(), tuiAction{kind: tuiActionDenyPermission}); err != nil {
		t.Fatal(err)
	}
	if allowed := <-second; allowed {
		t.Fatal("second request was allowed")
	}
	if model.permission.active() || controller.permission != nil {
		t.Fatalf("permission remains active: model=%+v request=%+v", model.permission, controller.permission)
	}
}

func TestTUIControllerPreservesClipboardInsertionPoint(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	clipboardImages := make(chan tuiEvent, 1)
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("before "); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		readClipboardImage: func(context.Context) (agent.Image, error) {
			close(started)
			<-release
			return agent.Image{MediaType: "image/png", Data: validTestPNG(t)}, nil
		},
		clipboardImages: clipboardImages,
		stopped:         make(chan struct{}),
	}

	if _, err := controller.applyAction(context.Background(), tuiAction{kind: tuiActionAttachImage}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := model.insertInput("after"); err != nil {
		t.Fatal(err)
	}
	close(release)
	event := <-clipboardImages
	if _, err := controller.transition(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	content := editorContent(model.input)
	if len(content) != 3 || content[0].Text != "before " || content[1].Image == nil || content[2].Text != "after" {
		t.Fatalf("content = %+v", content)
	}
}

func TestTUIControllerDiscardsClearedClipboardRead(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		readClipboardImage: func(ctx context.Context) (agent.Image, error) {
			close(started)
			<-ctx.Done()
			close(finished)
			return agent.Image{}, ctx.Err()
		},
		clipboardImages: make(chan tuiEvent, 1),
		stopped:         make(chan struct{}),
	}

	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyCtrlV}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyCtrlC}); err != nil {
		t.Fatal(err)
	}
	<-finished
	if len(model.input) != 0 || len(controller.clipboardRequests) != 0 {
		t.Fatalf("input = %+v, requests = %d", model.input, len(controller.clipboardRequests))
	}
}

func TestTUIControllerDeletingPendingImageCancelsRead(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		readClipboardImage: func(ctx context.Context) (agent.Image, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return agent.Image{}, ctx.Err()
		},
		clipboardImages: make(chan tuiEvent, 1),
		stopped:         make(chan struct{}),
	}
	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyCtrlV}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyBackspace}); err != nil {
		t.Fatal(err)
	}
	<-canceled
	if len(model.input) != 0 || len(controller.clipboardRequests) != 0 {
		t.Fatalf("input = %+v, requests = %d", model.input, len(controller.clipboardRequests))
	}
}

func TestTUIControllerDeleteCancelsPendingImage(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		readClipboardImage: func(ctx context.Context) (agent.Image, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return agent.Image{}, ctx.Err()
		},
		clipboardImages: make(chan tuiEvent, 1),
		stopped:         make(chan struct{}),
	}
	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyCtrlV}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyLeft}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyDelete}); err != nil {
		t.Fatal(err)
	}
	<-canceled
	if len(model.input) != 0 || len(controller.clipboardRequests) != 0 {
		t.Fatalf("input = %+v, requests = %d", model.input, len(controller.clipboardRequests))
	}
}

func TestTUIControllerHistoryCancelsPendingImage(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	model := newTUIModel(80, 24, Options{})
	model.history = []string{"old prompt"}
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		readClipboardImage: func(ctx context.Context) (agent.Image, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return agent.Image{}, ctx.Err()
		},
		clipboardImages: make(chan tuiEvent, 1),
		stopped:         make(chan struct{}),
	}

	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyCtrlV}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyUp}); err != nil {
		t.Fatal(err)
	}
	<-canceled
	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyDown}); err != nil {
		t.Fatal(err)
	}
	if model.inputText() != "draft" || len(model.pendingImageRequests()) != 0 || len(controller.clipboardRequests) != 0 {
		t.Fatalf("input = %q, pending = %v, requests = %d", model.inputText(), model.pendingImageRequests(), len(controller.clipboardRequests))
	}
}

func TestTUIControllerSubmittingCancelsPendingImage(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	launched := make(chan struct{})
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("before"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{runContentFunction: func(_ context.Context, content []agent.ContentPart, _ agent.EventSink) (agent.RunResult, error) {
			if len(content) != 1 || content[0].Text != "before" {
				t.Fatalf("content = %+v", content)
			}
			close(launched)
			return agent.RunResult{}, nil
		}}, output: io.Discard,
		engineMessages: make(chan engineMessage, 1),
		readClipboardImage: func(ctx context.Context) (agent.Image, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return agent.Image{}, ctx.Err()
		},
		clipboardImages: make(chan tuiEvent, 1),
		stopped:         make(chan struct{}),
	}
	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyCtrlV}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyEnter}); err != nil {
		t.Fatal(err)
	}
	<-canceled
	<-launched
	if !model.running || len(model.blocks) != 1 || model.inputText() != "" || len(controller.clipboardRequests) != 0 {
		t.Fatalf("running = %v, blocks = %+v, input = %q, requests = %d", model.running, model.blocks, model.inputText(), len(controller.clipboardRequests))
	}
}

func TestTUIControllerExitCancelsClipboardRead(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		readClipboardImage: func(ctx context.Context) (agent.Image, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return agent.Image{}, ctx.Err()
		},
		clipboardImages: make(chan tuiEvent, 1),
		stopped:         make(chan struct{}),
	}
	if _, err := controller.handleKey(context.Background(), keyEvent{code: keyCtrlV}); err != nil {
		t.Fatal(err)
	}
	<-started
	exit, err := controller.applyAction(context.Background(), tuiAction{kind: tuiActionExit})
	if err != nil || !exit {
		t.Fatalf("exit = %v, error = %v", exit, err)
	}
	<-canceled
}

func TestTUIControllerReadsClipboardImageWithoutBlocking(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	clipboardImages := make(chan tuiEvent, 1)
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		readClipboardImage: func(context.Context) (agent.Image, error) {
			close(started)
			<-release
			return agent.Image{MediaType: "image/png", Data: validTestPNG(t)}, nil
		},
		clipboardImages: clipboardImages,
	}

	if _, err := controller.applyAction(context.Background(), tuiAction{kind: tuiActionAttachImage}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("clipboard read did not start")
	}
	close(release)
	select {
	case event := <-clipboardImages:
		if _, err := controller.transition(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("clipboard read did not finish")
	}
}

func TestTUIControllerKeepsDraftWhenActiveCheckpointFails(t *testing.T) {
	checkpointErr := errors.New("checkpoint failed")
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("before "); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: validTestPNG(t)}); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &checkpointingFakeEngine{fakeEngine: &fakeEngine{}}, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
		saveCheckpoint: func(agent.Checkpoint, Checkpoint, bool) error { return checkpointErr },
	}

	action, err := reduceKey(model, keyEvent{code: keyEnter})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.applyAction(context.Background(), action); !errors.Is(err, checkpointErr) {
		t.Fatalf("error = %v", err)
	}
	if model.inputText() != "before " || model.imageCount() != 1 || model.running || len(model.blocks) != 0 {
		t.Fatalf("input = %q, images = %d, running = %v, blocks = %+v", model.inputText(), model.imageCount(), model.running, model.blocks)
	}
}

func TestTUIControllerActiveCheckpointOmitsUncommittedSubmission(t *testing.T) {
	image := &agent.Image{MediaType: "image/png", Data: validTestPNG(t)}
	content := []agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "describe "},
		{Kind: agent.ContentPartImage, Image: image},
	}
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &checkpointingFakeEngine{fakeEngine: &fakeEngine{}}, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
		saveCheckpoint: func(_ agent.Checkpoint, terminalCheckpoint Checkpoint, active bool) error {
			if !active || len(terminalCheckpoint.data.Blocks) != 0 {
				t.Fatalf("active=%v terminal=%+v", active, terminalCheckpoint.data)
			}
			return errors.New("stop before launch")
		},
	}

	if err := controller.startTurn(context.Background(), content); err == nil {
		t.Fatal("start succeeded")
	}
	if len(model.blocks) != 0 || model.running {
		t.Fatalf("blocks = %+v, running = %v", model.blocks, model.running)
	}
}

func TestTUIControllerSubmitsClipboardImageAfterCheckpoint(t *testing.T) {
	events := make(chan string, 2)
	engine := &fakeEngine{runContentFunction: func(_ context.Context, content []agent.ContentPart, _ agent.EventSink) (agent.RunResult, error) {
		if contentText(content) != "describe" || len(content) != 2 || content[1].Image == nil || content[1].Image.MediaType != "image/png" || !slices.Equal(content[1].Image.Data, validTestPNG(t)) {
			t.Fatalf("content=%+v", content)
		}
		events <- "launch"
		return agent.RunResult{}, nil
	}}
	model := newTUIModel(80, 24, Options{})
	messages := make(chan engineMessage, 1)
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &checkpointingFakeEngine{fakeEngine: engine}, output: io.Discard,
		engineMessages: messages, stopped: make(chan struct{}),
		saveCheckpoint: func(_ agent.Checkpoint, checkpoint Checkpoint, active bool) error {
			if !active || len(checkpoint.data.Blocks) != 0 {
				t.Fatalf("active=%v checkpoint=%+v", active, checkpoint.data)
			}
			events <- "checkpoint"
			return nil
		},
	}

	image := &agent.Image{MediaType: "image/png", Data: validTestPNG(t)}
	if _, err := controller.applyAction(context.Background(), tuiAction{
		kind: tuiActionSubmit,
		content: []agent.ContentPart{
			{Kind: agent.ContentPartText, Text: "describe"},
			{Kind: agent.ContentPartImage, Image: image},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if first, second := <-events, <-events; first != "checkpoint" || second != "launch" {
		t.Fatalf("events = %q, %q", first, second)
	}
	select {
	case message := <-messages:
		if !message.done || message.err != nil {
			t.Fatalf("message = %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("image turn did not finish")
	}
}

func TestTUIControllerEOFWhileRunningDefersCheckpoint(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.running = true
	canceled := false
	saveCalls := 0
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
		turnCancel: func() { canceled = true },
		saveCheckpoint: func(agent.Checkpoint, Checkpoint, bool) error {
			saveCalls++
			return nil
		},
	}

	exit, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEOF}})
	if err != nil || exit {
		t.Fatalf("transition exit=%v error=%v", exit, err)
	}
	if !canceled || !controller.exitAfterTurn || controller.exitAfterTurnErr != nil || saveCalls != 0 {
		t.Fatalf("canceled=%v exitAfterTurn=%v exitAfterTurnErr=%v saveCalls=%d", canceled, controller.exitAfterTurn, controller.exitAfterTurnErr, saveCalls)
	}
}

func TestTUIControllerStartsOperationsAfterActiveCheckpoint(t *testing.T) {
	tests := []struct {
		name          string
		prompt        string
		activity      activityKind
		wantUserBlock bool
		prepare       func(*tuiController)
		start         func(context.Context, *tuiController) error
	}{
		{
			name:          "submit",
			prompt:        "ordinary prompt",
			activity:      activityThinking,
			wantUserBlock: true,
			start: func(ctx context.Context, controller *tuiController) error {
				_, err := controller.applyAction(ctx, tuiAction{
					kind:    tuiActionSubmit,
					content: []agent.ContentPart{{Kind: agent.ContentPartText, Text: "ordinary prompt"}},
				})
				return err
			},
		},
		{
			name:          "goal",
			prompt:        "finish goal",
			activity:      activityThinking,
			wantUserBlock: true,
			start: func(ctx context.Context, controller *tuiController) error {
				_, err := controller.applyAction(ctx, tuiAction{kind: tuiActionSetGoal, prompt: "finish goal"})
				return err
			},
		},
		{
			name:     "compaction",
			activity: activityCompacting,
			start: func(ctx context.Context, controller *tuiController) error {
				_, err := controller.applyAction(ctx, tuiAction{kind: tuiActionCompact})
				return err
			},
		},
		{
			name:          "deferred replay",
			prompt:        "deferred prompt",
			activity:      activityThinking,
			wantUserBlock: true,
			prepare: func(controller *tuiController) {
				controller.deferredSteering = []string{"deferred prompt"}
				controller.model.queueSteering("deferred prompt")
			},
			start: func(ctx context.Context, controller *tuiController) error {
				return controller.startDeferredTurn(ctx)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := make(chan string, 2)
			engine := &fakeEngine{
				runContentFunction: func(context.Context, []agent.ContentPart, agent.EventSink) (agent.RunResult, error) {
					events <- "launch"
					return agent.RunResult{}, nil
				},
				compactFunction: func(context.Context, agent.EventSink) error {
					events <- "launch"
					return nil
				},
			}
			model := newTUIModel(80, 24, Options{})
			messages := make(chan engineMessage, 2)
			stopped := make(chan struct{})
			controller := tuiController{
				model: model, renderer: &tuiRenderer{}, engine: &checkpointingFakeEngine{fakeEngine: engine}, output: io.Discard,
				engineMessages: messages, stopped: stopped,
				saveCheckpoint: func(_ agent.Checkpoint, _ Checkpoint, active bool) error {
					if !active || !model.running || model.activity.kind != test.activity {
						t.Fatalf("checkpoint active=%v running=%v activity=%+v", active, model.running, model.activity)
					}
					events <- "checkpoint"
					return nil
				},
			}
			if test.prepare != nil {
				test.prepare(&controller)
			}

			if err := test.start(context.Background(), &controller); err != nil {
				t.Fatal(err)
			}
			if first := <-events; first != "checkpoint" {
				t.Fatalf("first operation = %q, want checkpoint", first)
			}
			if launched := <-events; launched != "launch" {
				t.Fatalf("launch operation = %q", launched)
			}
			if test.wantUserBlock {
				if len(model.blocks) != 1 || model.blocks[0].kind != blockUser || model.blocks[0].text != test.prompt {
					t.Fatalf("blocks = %+v", model.blocks)
				}
			} else if len(model.blocks) != 0 {
				t.Fatalf("blocks = %+v", model.blocks)
			}
			select {
			case message := <-messages:
				if !message.done || message.err != nil {
					t.Fatalf("engine message = %+v", message)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("operation did not finish")
			}
			close(stopped)
		})
	}
}

func TestTUIControllerDoesNotLaunchAfterActiveCheckpointFailure(t *testing.T) {
	checkpointErr := errors.New("checkpoint failed")
	tests := []struct {
		name     string
		activity activityKind
		prepare  func(*tuiController)
		start    func(context.Context, *tuiController) error
	}{
		{
			name:     "submit",
			activity: activityThinking,
			start: func(ctx context.Context, controller *tuiController) error {
				_, err := controller.applyAction(ctx, tuiAction{
					kind:    tuiActionSubmit,
					content: []agent.ContentPart{{Kind: agent.ContentPartText, Text: "ordinary prompt"}},
				})
				return err
			},
		},
		{
			name:     "goal",
			activity: activityThinking,
			start: func(ctx context.Context, controller *tuiController) error {
				_, err := controller.applyAction(ctx, tuiAction{kind: tuiActionSetGoal, prompt: "finish goal"})
				return err
			},
		},
		{
			name:     "compaction",
			activity: activityCompacting,
			start: func(ctx context.Context, controller *tuiController) error {
				_, err := controller.applyAction(ctx, tuiAction{kind: tuiActionCompact})
				return err
			},
		},
		{
			name:     "deferred replay",
			activity: activityThinking,
			prepare: func(controller *tuiController) {
				controller.deferredSteering = []string{"deferred prompt"}
				controller.model.queueSteering("deferred prompt")
			},
			start: func(ctx context.Context, controller *tuiController) error {
				return controller.startDeferredTurn(ctx)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			launched := make(chan struct{}, 1)
			engine := &fakeEngine{
				runContentFunction: func(context.Context, []agent.ContentPart, agent.EventSink) (agent.RunResult, error) {
					launched <- struct{}{}
					return agent.RunResult{}, nil
				},
				compactFunction: func(context.Context, agent.EventSink) error {
					launched <- struct{}{}
					return nil
				},
			}
			model := newTUIModel(80, 24, Options{})
			controller := tuiController{
				model: model, renderer: &tuiRenderer{}, engine: &checkpointingFakeEngine{fakeEngine: engine}, output: io.Discard,
				engineMessages: make(chan engineMessage, 2), stopped: make(chan struct{}),
				saveCheckpoint: func(agent.Checkpoint, Checkpoint, bool) error { return checkpointErr },
			}
			if test.prepare != nil {
				test.prepare(&controller)
			}

			if err := test.start(context.Background(), &controller); !errors.Is(err, checkpointErr) {
				t.Fatalf("start error = %v", err)
			}
			select {
			case <-launched:
				t.Fatal("operation launched after checkpoint failure")
			case <-time.After(25 * time.Millisecond):
			}
			wantRunning := test.name == "compaction"
			wantActivity := activityReady
			if wantRunning {
				wantActivity = test.activity
			}
			if controller.turnCancel != nil || model.running != wantRunning || model.activity.kind != wantActivity {
				t.Fatalf("cancel=%v running=%v activity=%+v", controller.turnCancel != nil, model.running, model.activity)
			}
		})
	}
}

func TestTUIControllerNewSessionKeepsCurrentConversation(t *testing.T) {
	engine := &fakeEngine{}
	model := newTUIModel(80, 24, Options{})
	model.appendBlock(blockAssistant, "keep me")
	if err := model.insertInput("/new"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	exit, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}})
	if err != nil || !exit || controller.outcome.Action != RunNewSession {
		t.Fatalf("transition exit=%v outcome=%+v error=%v", exit, controller.outcome, err)
	}
	if len(model.blocks) != 1 || model.blocks[0].text != "keep me" {
		t.Fatalf("blocks = %+v", model.blocks)
	}
}

func TestTUIControllerCompactsConversation(t *testing.T) {
	engine := &fakeEngine{compactFunction: func(_ context.Context, sink agent.EventSink) error {
		if err := sink(agent.Event{Kind: agent.EventCompactionStart}); err != nil {
			return err
		}
		return sink(agent.Event{Kind: agent.EventCompactionEnd, Usage: agent.Usage{TotalTokens: 100}})
	}}
	messages := make(chan engineMessage, 3)
	stopped := make(chan struct{})
	defer close(stopped)
	model := newTUIModel(80, 24, Options{})
	model.appendBlock(blockAssistant, "existing conversation")
	model.contextTokens = 90
	if err := model.insertInput("/compact"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: messages, stopped: stopped,
	}
	ctx := context.Background()

	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}}); err != nil {
		t.Fatal(err)
	}
	if !model.running || model.activity.kind != activityCompacting {
		t.Fatalf("running=%v activity=%+v", model.running, model.activity)
	}
	for range 3 {
		select {
		case message := <-messages:
			if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventEngine, engine: message}); err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("compaction did not complete")
		}
	}
	if engine.compactionCount() != 1 || model.running || model.activity.kind != activityReady || model.contextTokens != 0 {
		t.Fatalf("compactions=%d running=%v activity=%+v context=%d", engine.compactionCount(), model.running, model.activity, model.contextTokens)
	}
	if len(model.blocks) != 2 || model.blocks[0].text != "existing conversation" || model.blocks[1].kind != blockContext || model.blocks[1].text != "Compacting conversation" {
		t.Fatalf("blocks = %+v", model.blocks)
	}
}

func TestTUIControllerSetsShowsAndClearsGoal(t *testing.T) {
	engine := &fakeEngine{}
	messages := make(chan engineMessage, 1)
	stopped := make(chan struct{})
	defer close(stopped)
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: messages, stopped: stopped,
	}
	ctx := context.Background()

	if _, err := controller.applyAction(ctx, tuiAction{kind: tuiActionShowGoal}); err != nil {
		t.Fatal(err)
	}
	if len(model.blocks) != 1 || model.blocks[0].text != "No goal is set" {
		t.Fatalf("blocks = %+v", model.blocks)
	}

	if _, err := controller.applyAction(ctx, tuiAction{kind: tuiActionSetGoal, prompt: "finish migration"}); err != nil {
		t.Fatal(err)
	}
	goal, ok := engine.Goal()
	if !ok || goal.Objective != "finish migration" || !model.running {
		t.Fatalf("goal=%+v exists=%v running=%v", goal, ok, model.running)
	}
	select {
	case <-messages:
	case <-time.After(2 * time.Second):
		t.Fatal("goal turn did not complete")
	}
	calls := engine.snapshot()
	if !slices.Equal(calls, []string{"finish migration"}) {
		t.Fatalf("calls = %q", calls)
	}

	if _, err := controller.applyAction(ctx, tuiAction{kind: tuiActionShowGoal}); err != nil {
		t.Fatal(err)
	}
	if model.blocks[len(model.blocks)-1].text != "Goal: finish migration" {
		t.Fatalf("goal status block = %+v", model.blocks[len(model.blocks)-1])
	}

	if _, err := controller.applyAction(ctx, tuiAction{kind: tuiActionClearGoal}); err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.Goal(); ok || model.blocks[len(model.blocks)-1].text != "Goal cleared" {
		t.Fatalf("goal still set or wrong block: %+v", model.blocks[len(model.blocks)-1])
	}
}

func TestTUIControllerShowsHelpAndGoalWhileRunning(t *testing.T) {
	engine := &fakeEngine{goal: &agent.GoalState{Objective: "finish migration"}}
	model := newTUIModel(80, 24, Options{})
	model.running = true
	checkpointCalls := 0
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
		saveCheckpoint: func(agent.Checkpoint, Checkpoint, bool) error {
			checkpointCalls++
			return nil
		},
	}

	for _, command := range []string{"/help", "/goal"} {
		if err := model.insertInput(command); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}}); err != nil {
			t.Fatal(err)
		}
	}
	if !model.running || checkpointCalls != 0 || len(model.blocks) != 2 {
		t.Fatalf("running=%v checkpoints=%d blocks=%+v", model.running, checkpointCalls, model.blocks)
	}
	if !strings.Contains(model.blocks[0].text, "Commands:") || model.blocks[1].text != "Goal: finish migration" {
		t.Fatalf("blocks=%+v", model.blocks)
	}
}

func TestTUIControllerSetsGoalWhileRunning(t *testing.T) {
	engine := &fakeEngine{}
	model := newTUIModel(80, 24, Options{})
	model.running = true
	if err := model.insertInput("/goal finish migration"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}}); err != nil {
		t.Fatal(err)
	}
	goal, ok := engine.Goal()
	last := model.blocks[len(model.blocks)-1]
	if !ok || goal.Objective != "finish migration" || !model.running || last.kind != blockInfo || last.text != "Goal set: finish migration" {
		t.Fatalf("goal=%+v exists=%v running=%v block=%+v", goal, ok, model.running, last)
	}
	if calls := engine.snapshot(); len(calls) != 0 {
		t.Fatalf("goal update started runs: %q", calls)
	}
}

func TestTUIControllerClearsGoalWhileRunning(t *testing.T) {
	engine := &fakeEngine{goal: &agent.GoalState{Objective: "finish migration"}}
	model := newTUIModel(80, 24, Options{})
	model.running = true
	if err := model.insertInput("/goal clear"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.Goal(); ok {
		t.Fatal("goal survived running clear command")
	}
	last := model.blocks[len(model.blocks)-1]
	if !model.running || last.kind != blockInfo || last.text != "Goal cleared" {
		t.Fatalf("running=%v block=%+v", model.running, last)
	}
}

func TestTUIControllerDoesNotStartGoalWhenSetFails(t *testing.T) {
	setErr := errors.New("invalid goal")
	engine := &fakeEngine{setGoalErr: setErr}
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	if _, err := controller.applyAction(context.Background(), tuiAction{kind: tuiActionSetGoal, prompt: "goal"}); err != nil {
		t.Fatal(err)
	}
	calls := engine.snapshot()
	if len(calls) != 0 || model.running || model.activity.kind != activityError || model.activity.detail != setErr.Error() {
		t.Fatalf("calls=%q running=%v activity=%+v", calls, model.running, model.activity)
	}
}

func TestTUIControllerNewSessionLeavesCurrentGoal(t *testing.T) {
	engine := &fakeEngine{goal: &agent.GoalState{Objective: "goal"}}
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	exit, err := controller.applyAction(context.Background(), tuiAction{kind: tuiActionNewSession})
	if err != nil || !exit || controller.outcome.Action != RunNewSession {
		t.Fatalf("new session exit=%v outcome=%+v error=%v", exit, controller.outcome, err)
	}
	if _, ok := engine.Goal(); !ok {
		t.Fatal("current goal was cleared")
	}
}

func TestTUIControllerAppliesSubagentStatus(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard}

	_, err := controller.transition(context.Background(), tuiEvent{
		kind: tuiEventSubagentStatus,
		subagentStatus: SubagentStatus{
			Running: 2, Finalizing: 1,
			Active: []SubagentJobStatus{
				{ID: "subagent-1\nignored", Task: "inspect\nignored", State: SubagentRunning, Generations: -1},
				{ID: "subagent-invalid", State: SubagentState("invalid")},
			},
			Awaiting: []SubagentCompletionStatus{
				{SubagentID: "subagent-2", Task: "finished", State: SubagentComplete},
				{SubagentID: "subagent-3", Task: "failed", State: SubagentFailed},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if model.subagentStatus.Running != 2 || model.subagentStatus.Finalizing != 1 || len(model.subagentStatus.Awaiting) != 2 || len(model.subagentStatus.Active) != 1 || model.subagentStatus.Active[0].ID != "subagent-1 ignored" || model.subagentStatus.Active[0].Task != "inspect ignored" || model.subagentStatus.Active[0].Generations != 0 || model.subagentStatus.Awaiting[0].State != SubagentComplete || model.subagentStatus.Awaiting[1].State != SubagentFailed || !controller.dirty {
		t.Fatalf("status=%+v dirty=%v", model.subagentStatus, controller.dirty)
	}

	_, err = controller.transition(context.Background(), tuiEvent{
		kind:           tuiEventSubagentStatus,
		subagentStatus: SubagentStatus{Running: -1, Finalizing: -1, Awaiting: nil},
	})
	if err != nil || model.subagentStatus.Running != 0 || model.subagentStatus.Finalizing != 0 || len(model.subagentStatus.Awaiting) != 0 || len(model.subagentStatus.Active) != 0 {
		t.Fatalf("sanitized status=%+v error=%v", model.subagentStatus, err)
	}
}

func TestTUIControllerEventDirtiness(t *testing.T) {
	tests := []struct {
		name      string
		prepare   func(*tuiModel)
		event     tuiEvent
		wantDirty bool
	}{
		{
			name:      "provider usage error still redraws",
			event:     tuiEvent{kind: tuiEventProviderUsage, providerUsage: providerUsageMessage{err: errors.New("usage failed")}},
			wantDirty: true,
		},
		{
			name:      "stale file search is ignored",
			event:     tuiEvent{kind: tuiEventFileSearch, fileSearch: fileSearchResult{id: 1, paths: []string{"ignored"}}},
			wantDirty: false,
		},
		{
			name:      "idle spinner is ignored",
			event:     tuiEvent{kind: tuiEventSpinner},
			wantDirty: false,
		},
		{
			name: "active spinner redraws",
			prepare: func(model *tuiModel) {
				model.activity = activity{kind: activityThinking}
			},
			event:     tuiEvent{kind: tuiEventSpinner},
			wantDirty: true,
		},
		{
			name: "running subagent spinner redraws",
			prepare: func(model *tuiModel) {
				model.subagentStatus = SubagentStatus{Running: 1, Active: []SubagentJobStatus{{State: SubagentRunning}}}
			},
			event:     tuiEvent{kind: tuiEventSpinner},
			wantDirty: true,
		},
		{
			name: "completed subagent spinner is ignored",
			prepare: func(model *tuiModel) {
				model.subagentStatus = SubagentStatus{Awaiting: []SubagentCompletionStatus{{State: SubagentComplete}}}
			},
			event:     tuiEvent{kind: tuiEventSpinner},
			wantDirty: false,
		},
		{
			name:      "usage clock without reset is ignored",
			event:     tuiEvent{kind: tuiEventUsageClock},
			wantDirty: false,
		},
		{
			name: "usage clock with reset redraws",
			prepare: func(model *tuiModel) {
				model.providerUsage.Windows = []agent.UsageWindow{{ResetsAt: time.Unix(1, 0)}}
			},
			event:     tuiEvent{kind: tuiEventUsageClock},
			wantDirty: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newTUIModel(80, 24, Options{})
			if test.prepare != nil {
				test.prepare(model)
			}
			controller := tuiController{model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard}

			exit, err := controller.transition(context.Background(), test.event)
			if err != nil || exit || controller.dirty != test.wantDirty {
				t.Fatalf("exit=%v error=%v dirty=%v, want %v", exit, err, controller.dirty, test.wantDirty)
			}
		})
	}
}

func TestRenderFailureDoesNotCommitUnseenFrame(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	renderer := &tuiRenderer{}
	dirty := true
	if err := renderIfDirty(renderer, model, failingWriter{}, &dirty, false); !errors.Is(err, errOutput) {
		t.Fatalf("render error = %v", err)
	}
	if renderer.frame.width != 0 || !dirty {
		t.Fatalf("committed frame=%+v dirty=%v", renderer.frame, dirty)
	}
}

func TestCleanRenderSkipsViewportPreparation(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	for range 10 {
		model.appendBlock(blockInfo, "line")
	}
	renderer := &tuiRenderer{}
	dirty := false

	if err := renderIfDirty(renderer, model, failingWriter{}, &dirty, false); err != nil {
		t.Fatal(err)
	}
	if renderer.conversationVersion != 0 || len(renderer.conversationLines) != 0 || model.scrollTop != 0 {
		t.Fatalf("renderer version=%d lines=%d scroll=%d", renderer.conversationVersion, len(renderer.conversationLines), model.scrollTop)
	}
}

func TestMouseSelectionUsesCommittedFrame(t *testing.T) {
	model := newTUIModel(20, 10, Options{})
	model.appendBlock(blockAssistant, "alpha")
	renderer := &tuiRenderer{}
	_ = renderer.render(model)
	committed := renderer.frame

	model.clearConversation()
	model.appendBlock(blockAssistant, "new unseen text")
	reduceMouse(model, mouseEvent{kind: mousePress, column: 1, row: 1}, committed)
	action := reduceMouse(model, mouseEvent{kind: mouseRelease, column: 5, row: 1}, committed)
	if action.kind != tuiActionCopy || action.text != "alpha" || strings.Contains(action.text, "unseen") {
		t.Fatalf("action = %+v", action)
	}
}

func TestMouseWheelUsesCommittedConversationBounds(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	for range 8 {
		model.appendBlock(blockInfo, "visible line")
	}
	renderer := &tuiRenderer{}
	_ = renderer.render(model)
	committedTop := renderer.frame.conversationTop
	if committedTop < mouseWheelScrollLines {
		t.Fatalf("committed top = %d", committedTop)
	}
	for range 8 {
		model.appendBlock(blockInfo, "unseen line")
	}

	reduceMouse(model, mouseEvent{kind: mouseWheelUp}, renderer.frame)
	if model.scrollTop != committedTop-mouseWheelScrollLines {
		t.Fatalf("scroll top = %d, want %d", model.scrollTop, committedTop-mouseWheelScrollLines)
	}
}

func TestTUIControllerQueuesAndDequeuesSteering(t *testing.T) {
	var queued []string
	engine := &fakeEngine{
		steerFunction: func(prompt string) bool {
			queued = append(queued, prompt)
			return true
		},
		clearFunction: func() []string {
			messages := append([]string(nil), queued...)
			queued = nil
			return messages
		},
	}
	model := newTUIModel(80, 24, Options{})
	model.running = true
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	if err := model.insertInput("steer"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(queued, []string{"steer"}) || !slices.Equal(model.steering, []string{"steer"}) {
		t.Fatalf("engine queue=%q model queue=%q", queued, model.steering)
	}
	if calls := engine.snapshot(); len(calls) != 0 {
		t.Fatalf("steering started runs: %q", calls)
	}

	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyAltUp}}); err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 || len(model.steering) != 0 || model.inputText() != "steer\n\ndraft" {
		t.Fatalf("queue=%q model queue=%q input=%q", queued, model.steering, model.inputText())
	}
}

func TestTUIControllerCancelRestoresQueuedAndDeferredSteering(t *testing.T) {
	engine := &fakeEngine{clearFunction: func() []string { return []string{"accepted"} }}
	model := newTUIModel(80, 24, Options{})
	model.running = true
	model.queueSteering("accepted")
	model.queueSteering("deferred")
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	canceled := false
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
		deferredSteering: []string{"deferred"},
		turnCancel:       func() { canceled = true },
	}

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEscape}}); err != nil {
		t.Fatal(err)
	}
	if !canceled || !model.interrupted || model.activity.kind != activityCanceling || len(controller.deferredSteering) != 0 || len(model.steering) != 0 {
		t.Fatalf("canceled=%v interrupted=%v activity=%+v deferred=%q pending=%q", canceled, model.interrupted, model.activity, controller.deferredSteering, model.steering)
	}
	if model.inputText() != "accepted\n\ndeferred\n\ndraft" {
		t.Fatalf("restored input = %q", model.inputText())
	}
}

func TestTUIControllerRunsRejectedSteeringSequentially(t *testing.T) {
	steerCalls := 0
	engine := &fakeEngine{
		steerFunction: func(string) bool {
			steerCalls++
			return false
		},
	}
	messages := make(chan engineMessage, 4)
	stopped := make(chan struct{})
	defer close(stopped)
	model := newTUIModel(80, 24, Options{})
	model.running = true
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: messages, stopped: stopped,
	}
	ctx := context.Background()

	for _, prompt := range []string{"one", "two"} {
		if err := model.insertInput(prompt); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}}); err != nil {
			t.Fatal(err)
		}
	}
	if steerCalls != 1 || !slices.Equal(controller.deferredSteering, []string{"one", "two"}) {
		t.Fatalf("steer calls=%d deferred=%q", steerCalls, controller.deferredSteering)
	}

	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventEngine, engine: engineMessage{done: true}}); err != nil {
		t.Fatal(err)
	}
	if !model.running || !slices.Equal(controller.deferredSteering, []string{"two"}) {
		t.Fatalf("first replay running=%v deferred=%q", model.running, controller.deferredSteering)
	}
	var firstDone engineMessage
	select {
	case firstDone = <-messages:
	case <-time.After(2 * time.Second):
		t.Fatal("first deferred turn did not complete")
	}
	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventEngine, engine: firstDone}); err != nil {
		t.Fatal(err)
	}
	var secondDone engineMessage
	select {
	case secondDone = <-messages:
	case <-time.After(2 * time.Second):
		t.Fatal("second deferred turn did not complete")
	}
	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventEngine, engine: secondDone}); err != nil {
		t.Fatal(err)
	}
	calls := engine.snapshot()
	if !slices.Equal(calls, []string{"one", "two"}) || model.running || len(model.steering) != 0 {
		t.Fatalf("calls=%q running=%v pending=%q", calls, model.running, model.steering)
	}
}

func TestTUIControllerRestoresSteeringAfterRunError(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.running = true
	model.queueSteering("retry this")
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	failure := errors.New("failed")
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventEngine, engine: engineMessage{done: true, err: failure}}); err != nil {
		t.Fatal(err)
	}
	if model.inputText() != "retry this" || len(model.steering) != 0 || model.activity.kind != activityError {
		t.Fatalf("input=%q steering=%q activity=%+v", model.inputText(), model.steering, model.activity)
	}
}

func TestTUIControllerTogglesFastMode(t *testing.T) {
	var configured []bool
	model := newTUIModel(80, 24, Options{FastModeAvailable: true, SetFastMode: func(bool) error { return nil }})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
		setFastMode: func(enabled bool) error {
			configured = append(configured, enabled)
			return nil
		},
		saveCheckpoint: nil,
	}
	for _, want := range []bool{true, false} {
		if _, err := controller.applyAction(context.Background(), tuiAction{kind: tuiActionToggleFast}); err != nil {
			t.Fatal(err)
		}
		if model.fastMode != want {
			t.Fatalf("fast mode = %v, want %v", model.fastMode, want)
		}
	}
	if !slices.Equal(configured, []bool{true, false}) {
		t.Fatalf("configured=%v", configured)
	}
	if len(model.blocks) != 2 || model.blocks[0].text != "Fast mode on" || model.blocks[1].text != "Fast mode off" {
		t.Fatalf("blocks = %+v", model.blocks)
	}
}

func TestTUIControllerTogglesFastModeWhileRunning(t *testing.T) {
	var configured bool
	model := newTUIModel(80, 24, Options{FastModeAvailable: true, SetFastMode: func(bool) error { return nil }})
	model.running = true
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
		setFastMode: func(enabled bool) error {
			configured = enabled
			return nil
		},
		saveCheckpoint: func(agent.Checkpoint, Checkpoint, bool) error {
			t.Fatal("checkpoint saved while running")
			return nil
		},
	}
	if _, err := controller.applyAction(context.Background(), tuiAction{kind: tuiActionToggleFast}); err != nil {
		t.Fatal(err)
	}
	if !configured || !model.fastMode || !model.running {
		t.Fatalf("configured=%v fast=%v running=%v", configured, model.fastMode, model.running)
	}
}

func TestTUIControllerAppliesThinkingLevelWhileRunning(t *testing.T) {
	var configured agent.ThinkingLevel
	checkpointCalls := 0
	model := newTUIModel(80, 24, Options{SetThinkingLevel: func(agent.ThinkingLevel) error { return nil }})
	model.running = true
	model.activity = activity{kind: activityThinking}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
		setThinkingLevel: func(level agent.ThinkingLevel) error {
			configured = level
			return nil
		},
		saveCheckpoint: func(agent.Checkpoint, Checkpoint, bool) error {
			checkpointCalls++
			return nil
		},
	}
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyShiftTab}}); err != nil {
		t.Fatal(err)
	}
	if configured != agent.ThinkingHigh || model.thinkingLevel != agent.ThinkingHigh || !model.running || model.activity.kind != activityThinking || checkpointCalls != 0 {
		t.Fatalf("configured=%q model=%q running=%v activity=%+v checkpoints=%d", configured, model.thinkingLevel, model.running, model.activity, checkpointCalls)
	}
}

func TestTUIControllerAppliesThinkingLevelOutsideModel(t *testing.T) {
	var configured agent.ThinkingLevel
	model := newTUIModel(80, 24, Options{SetThinkingLevel: func(agent.ThinkingLevel) error { return nil }})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
		setThinkingLevel: func(level agent.ThinkingLevel) error {
			configured = level
			return nil
		},
	}
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyShiftTab}}); err != nil {
		t.Fatal(err)
	}
	if configured != agent.ThinkingHigh || model.thinkingLevel != agent.ThinkingHigh {
		t.Fatalf("configured=%q model=%q", configured, model.thinkingLevel)
	}
}
