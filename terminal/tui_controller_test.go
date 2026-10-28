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
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/skill"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal/keys"
)

func TestTUIControllerFlushesMessageHistory(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	model.rememberPrompt("first prompt")
	model.rememberPrompt(" /resume ")
	model.rememberPrompt("second prompt")

	var entries []string
	controller := tuiController{
		model: model,
		messageHistory: MessageHistory{Append: func(prompt string) error {
			entries = append(entries, prompt)
			return nil
		}},
	}
	if err := controller.flushMessageHistory(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(entries, []string{"first prompt", " /resume ", "second prompt"}) || len(model.pendingHistory) != 0 {
		t.Fatalf("entries = %q, pending = %q", entries, model.pendingHistory)
	}
	if !slices.Equal(model.history, []string{"first prompt", " /resume ", "second prompt"}) {
		t.Fatalf("local history = %q", model.history)
	}
}

func TestTUIControllerPastesClipboardImage(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	clipboardImages := make(chan tuiEvent, 1)
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard,
		stopped:           make(chan struct{}),
		clipboardRequests: map[uint64]context.CancelFunc{},
		readClipboardImage: func(context.Context) (agent.Image, error) {
			return agent.Image{MediaType: "image/png", Data: validTestPNG(t)}, nil
		},
		clipboardImages: clipboardImages,
	}

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.CtrlV}}); err != nil {
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

func TestTUIControllerQueuesClipboardImageWhileRunning(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	setRunning(model)
	model.activity = activity{kind: activityThinking}
	if err := model.insertInput("describe "); err != nil {
		t.Fatal(err)
	}
	clipboardImages := make(chan tuiEvent, 1)
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard,
		clipboardRequests: map[uint64]context.CancelFunc{},
		readClipboardImage: func(context.Context) (agent.Image, error) {
			return agent.Image{MediaType: "image/png", Data: validTestPNG(t)}, nil
		},
		clipboardImages: clipboardImages,
		stopped:         make(chan struct{}),
	}

	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.CtrlV}); err != nil {
		t.Fatal(err)
	}
	event := <-clipboardImages
	if _, err := controller.transition(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := model.insertInput(" please"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.Enter}); err != nil {
		t.Fatal(err)
	}

	if len(model.queued) != 1 {
		t.Fatalf("queued = %+v", model.queued)
	}
	queued := model.queued[0]
	if len(queued) != 3 || queued[0].Text != "describe " || queued[1].Image == nil || queued[2].Text != " please" {
		t.Fatalf("queued content = %+v", queued)
	}
	if !slices.Equal(queued[1].Image.Data, validTestPNG(t)) || len(model.input) != 0 || model.activity.kind != activityThinking {
		t.Fatalf("image = %+v, input = %+v, activity = %+v", queued[1].Image, model.input, model.activity)
	}
}

func TestTUIControllerSerializesPermissions(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	setRunning(model)
	controller := tuiController{model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard}
	first := make(chan PermissionDecision, 1)
	second := make(chan PermissionDecision, 1)

	for _, request := range []PermissionRequest{
		{Title: "Network access", Detail: "git push", Response: first},
		{Title: "Network access", Detail: "ssh host", Response: second},
	} {
		if _, err := controller.handlePermission(request); err != nil {
			t.Fatal(err)
		}
	}
	if model.permission.view().detail != "git push" || len(model.permission.queue) != 2 {
		t.Fatalf("permission=%+v", model.permission)
	}

	if _, err := controller.applyAction(context.Background(), permissionAction(PermissionAllowOnce)); err != nil {
		t.Fatal(err)
	}
	if decision := <-first; decision != PermissionAllowOnce {
		t.Fatalf("first decision = %d", decision)
	}
	if model.permission.view().detail != "ssh host" || len(model.permission.queue) != 1 {
		t.Fatalf("next permission = %+v", model.permission)
	}

	if _, err := controller.applyAction(context.Background(), permissionAction(PermissionDenyOnce)); err != nil {
		t.Fatal(err)
	}
	if decision := <-second; decision != PermissionDenyOnce {
		t.Fatalf("second decision = %d", decision)
	}
	if model.permission.active() {
		t.Fatalf("permission remains active: %+v", model.permission)
	}
}

func TestTUIControllerAllowsPermissionsForSession(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	setRunning(model)
	controller := tuiController{model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard}
	first := make(chan PermissionDecision, 1)
	second := make(chan PermissionDecision, 1)

	for _, response := range []chan PermissionDecision{first, second} {
		if _, err := controller.handlePermission(PermissionRequest{Title: "Network access", Response: response}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := controller.applyAction(context.Background(), permissionAction(PermissionAllowSession)); err != nil {
		t.Fatal(err)
	}
	for index, response := range []chan PermissionDecision{first, second} {
		if decision := <-response; decision != PermissionAllowSession {
			t.Fatalf("decision %d = %d", index, decision)
		}
	}
	if model.permission.active() {
		t.Fatalf("permission remains active: %+v", model.permission)
	}

	future := make(chan PermissionDecision, 1)
	if _, err := controller.handlePermission(PermissionRequest{Title: "Network access", Response: future}); err != nil {
		t.Fatal(err)
	}
	if decision := <-future; decision != PermissionAllowSession {
		t.Fatalf("future decision = %d", decision)
	}
	if model.permission.active() {
		t.Fatalf("future permission was shown: %+v", model.permission)
	}
}

func TestTUIControllerHandlesPermissionWhileIdle(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	model.activity = activity{kind: activityError, detail: "previous error"}
	controller := tuiController{model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard}
	response := make(chan PermissionDecision, 1)

	if _, err := controller.handlePermission(PermissionRequest{Title: "Network access", Response: response}); err != nil {
		t.Fatal(err)
	}
	select {
	case decision := <-response:
		t.Fatalf("permission resolved before input: %d", decision)
	default:
	}
	if _, status, _ := renderStatus(model, model.width); !model.permission.active() || status != "waiting for permission" || model.activity.kind != activityError {
		t.Fatalf("permission=%+v activity=%+v", model.permission, model.activity)
	}

	controller.resolvePermission(PermissionDenyOnce)
	if decision := <-response; decision != PermissionDenyOnce {
		t.Fatalf("decision = %d", decision)
	}
	if model.permission.active() || model.activity.kind != activityError || model.activity.detail != "previous error" {
		t.Fatalf("permission=%+v activity=%+v", model.permission, model.activity)
	}
}

func TestTUIControllerKeepsPermissionAfterMainTurnFinishes(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	setRunning(model)
	model.activity = activity{kind: activityThinking}
	controller := tuiController{model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard}
	response := make(chan PermissionDecision, 1)

	if _, err := controller.handlePermission(PermissionRequest{Title: "Network access", Response: response}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.transition(context.Background(), sessionEvent(testSession(model).settle(nil))); err != nil {
		t.Fatal(err)
	}
	select {
	case decision := <-response:
		t.Fatalf("permission resolved when main turn finished: %d", decision)
	default:
	}
	if _, status, _ := renderStatus(model, model.width); model.running || !model.permission.active() || status != "waiting for permission" {
		t.Fatalf("running=%t permission=%+v activity=%+v", model.running, model.permission, model.activity)
	}

	controller.resolvePermission(PermissionAllowOnce)
	if decision := <-response; decision != PermissionAllowOnce {
		t.Fatalf("decision = %d", decision)
	}
	if model.permission.active() || model.activity.kind != activityReady {
		t.Fatalf("permission=%+v activity=%+v", model.permission, model.activity)
	}
}

func TestTUIControllerPreservesClipboardInsertionPoint(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	clipboardImages := make(chan tuiEvent, 1)
	model := newTestModel(80, 24, Options{})
	if err := model.insertInput("before "); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard,
		clipboardRequests: map[uint64]context.CancelFunc{},
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
	model := newTestModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard,
		clipboardRequests: map[uint64]context.CancelFunc{},
		readClipboardImage: func(ctx context.Context) (agent.Image, error) {
			close(started)
			<-ctx.Done()
			close(finished)
			return agent.Image{}, ctx.Err()
		},
		clipboardImages: make(chan tuiEvent, 1),
		stopped:         make(chan struct{}),
	}

	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.CtrlV}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.CtrlC}); err != nil {
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
	model := newTestModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard,
		clipboardRequests: map[uint64]context.CancelFunc{},
		readClipboardImage: func(ctx context.Context) (agent.Image, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return agent.Image{}, ctx.Err()
		},
		clipboardImages: make(chan tuiEvent, 1),
		stopped:         make(chan struct{}),
	}
	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.CtrlV}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.Backspace}); err != nil {
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
	model := newTestModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard,
		clipboardRequests: map[uint64]context.CancelFunc{},
		readClipboardImage: func(ctx context.Context) (agent.Image, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return agent.Image{}, ctx.Err()
		},
		clipboardImages: make(chan tuiEvent, 1),
		stopped:         make(chan struct{}),
	}
	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.CtrlV}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.Left}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.Delete}); err != nil {
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
	model := newTestModel(80, 24, Options{})
	model.history = []string{"old prompt"}
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard,
		clipboardRequests: map[uint64]context.CancelFunc{},
		readClipboardImage: func(ctx context.Context) (agent.Image, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return agent.Image{}, ctx.Err()
		},
		clipboardImages: make(chan tuiEvent, 1),
		stopped:         make(chan struct{}),
	}

	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.CtrlV}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.Up}); err != nil {
		t.Fatal(err)
	}
	<-canceled
	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.Down}); err != nil {
		t.Fatal(err)
	}
	if model.inputText() != "draft" || len(model.pendingImageRequests()) != 0 || len(controller.clipboardRequests) != 0 {
		t.Fatalf("input = %q, pending = %v, requests = %d", model.inputText(), model.pendingImageRequests(), len(controller.clipboardRequests))
	}
}

func TestTUIControllerSubmittingCancelsPendingImage(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	model := newTestModel(80, 24, Options{})
	if err := model.insertInput("before"); err != nil {
		t.Fatal(err)
	}
	controller := newTestController(model)
	controller.readClipboardImage = func(ctx context.Context) (agent.Image, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return agent.Image{}, ctx.Err()
	}
	controller.clipboardImages = make(chan tuiEvent, 1)
	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.CtrlV}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.Enter}); err != nil {
		t.Fatal(err)
	}
	<-canceled
	sent := testSession(model).sent
	if len(sent) != 1 || len(sent[0]) != 1 || sent[0][0].Text != "before" {
		t.Fatalf("sent = %+v", sent)
	}
	if !model.running || len(model.entries) != 1 || model.inputText() != "" || len(controller.clipboardRequests) != 0 {
		t.Fatalf("running = %v, blocks = %+v, input = %q, requests = %d", model.running, model.entries, model.inputText(), len(controller.clipboardRequests))
	}
}

func TestTUIControllerExitCancelsClipboardRead(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	model := newTestModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard,
		clipboardRequests: map[uint64]context.CancelFunc{},
		readClipboardImage: func(ctx context.Context) (agent.Image, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return agent.Image{}, ctx.Err()
		},
		clipboardImages: make(chan tuiEvent, 1),
		stopped:         make(chan struct{}),
	}
	if _, err := controller.handleKey(context.Background(), keys.Event{Code: keys.CtrlV}); err != nil {
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
	model := newTestModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard,
		clipboardRequests: map[uint64]context.CancelFunc{},
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

func TestTUIControllerKeepsDraftWhenTurnCannotStart(t *testing.T) {
	startErr := errors.New("save failed")
	model := newTestModel(80, 24, Options{})
	if err := model.insertInput("before "); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: validTestPNG(t)}); err != nil {
		t.Fatal(err)
	}
	testSession(model).startErr = startErr
	controller := newTestController(model)

	action, err := reduceKey(model, keys.Event{Code: keys.Enter})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.applyAction(context.Background(), action); !errors.Is(err, startErr) {
		t.Fatalf("error = %v", err)
	}
	if model.inputText() != "before " || model.imageCount() != 1 || model.running || len(model.entries) != 0 {
		t.Fatalf("input = %q, images = %d, running = %v, blocks = %+v", model.inputText(), model.imageCount(), model.running, model.entries)
	}
}

func TestTUIControllerSubmitsClipboardImage(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	controller := newTestController(model)

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
	sent := testSession(model).sent
	if len(sent) != 1 || contentText(sent[0]) != "describe" || len(sent[0]) != 2 || sent[0][1].Image == nil || !slices.Equal(sent[0][1].Image.Data, validTestPNG(t)) {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestTUIControllerEOFWhileRunningDefersSave(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	setRunning(model)
	saveCalls := 0
	onSave(model, func(session.Draft) error {
		saveCalls++
		return nil
	})
	controller := newTestController(model)

	exit, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.EOF}})
	if err != nil || exit {
		t.Fatalf("transition exit=%v error=%v", exit, err)
	}
	if !testSession(model).interrupted || !controller.exitAfterTurn || controller.exitAfterTurnErr != nil || saveCalls != 0 {
		t.Fatalf("interrupted=%v exitAfterTurn=%v exitAfterTurnErr=%v saveCalls=%d", testSession(model).interrupted, controller.exitAfterTurn, controller.exitAfterTurnErr, saveCalls)
	}

	exit, err = controller.transition(context.Background(), sessionEvent(testSession(model).settle(context.Canceled)))
	if err != nil || !exit || saveCalls != 0 {
		t.Fatalf("settled exit=%v error=%v saveCalls=%d", exit, err, saveCalls)
	}
}

func TestTUIControllerStartsOperations(t *testing.T) {
	tests := []struct {
		name     string
		action   tuiAction
		activity activityKind
		sent     []string
	}{
		{
			name:     "submit",
			action:   tuiAction{kind: tuiActionSubmit, content: testTextContent("ordinary prompt")},
			activity: activityThinking,
			sent:     []string{"ordinary prompt"},
		},
		{
			name:     "goal",
			action:   commandAction("/goal", "finish goal"),
			activity: activityThinking,
			sent:     []string{"finish goal"},
		},
		{
			name:     "compaction",
			action:   commandAction("/compact", ""),
			activity: activityCompacting,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newTestModel(80, 24, Options{})
			controller := newTestController(model)

			if _, err := controller.applyAction(context.Background(), test.action); err != nil {
				t.Fatal(err)
			}
			sent := contentTexts(testSession(model).sent)
			if !model.running || model.activity.kind != test.activity || !slices.Equal(sent, test.sent) {
				t.Fatalf("running=%v activity=%+v sent=%q", model.running, model.activity, sent)
			}
			if len(model.entries) != len(test.sent) {
				t.Fatalf("blocks = %+v", model.entries)
			}
		})
	}
}

func TestTUIControllerStaysIdleWhenOperationCannotStart(t *testing.T) {
	startErr := errors.New("save failed")
	for _, action := range []tuiAction{
		{kind: tuiActionSubmit, content: testTextContent("ordinary prompt")},
		commandAction("/goal", "finish goal"),
		commandAction("/compact", ""),
	} {
		model := newTestModel(80, 24, Options{})
		testSession(model).startErr = startErr
		controller := newTestController(model)

		if _, err := controller.applyAction(context.Background(), action); !errors.Is(err, startErr) {
			t.Fatalf("start error = %v", err)
		}
		if model.running || model.activity.kind != activityReady || len(model.entries) != 0 {
			t.Fatalf("running=%v activity=%+v blocks=%+v", model.running, model.activity, model.entries)
		}
	}
}

func TestTUIControllerSendsSkillCommandTextToTurn(t *testing.T) {
	model := newTestModel(80, 24, Options{Config: Config{Skills: []skill.Skill{{Name: "review"}}}})
	if err := model.insertInput("/skill:review check tests"); err != nil {
		t.Fatal(err)
	}
	controller := newTestController(model)

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.Enter}}); err != nil {
		t.Fatal(err)
	}
	if sent := contentTexts(testSession(model).sent); !slices.Equal(sent, []string{"/skill:review check tests"}) {
		t.Fatalf("sent = %q", sent)
	}
	if len(model.entries) != 1 || model.entries[0].Kind != session.EntryUser || model.entries[0].Text != "/skill:review check tests" {
		t.Fatalf("blocks = %+v", model.entries)
	}
}

func TestTUIControllerNewSessionKeepsCurrentConversation(t *testing.T) {
	engine := &fakeEngine{}
	model := newTestModel(80, 24, Options{})
	model.appendEntry(session.EntryAssistant, "keep me")
	if err := model.insertInput("/new"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(engine), output: io.Discard,
		stopped: make(chan struct{}),
	}

	exit, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.Enter}})
	if err != nil || !exit || controller.outcome.Action != RunNewSession {
		t.Fatalf("transition exit=%v outcome=%+v error=%v", exit, controller.outcome, err)
	}
	if len(model.entries) != 1 || model.entries[0].Text != "keep me" {
		t.Fatalf("blocks = %+v", model.entries)
	}
}

func TestTUIControllerCompactsConversation(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	model.appendEntry(session.EntryAssistant, "existing conversation")
	model.contextTokens = 90
	if err := model.insertInput("/compact"); err != nil {
		t.Fatal(err)
	}
	controller := newTestController(model)
	ctx := context.Background()

	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.Enter}}); err != nil {
		t.Fatal(err)
	}
	if !model.running || model.activity.kind != activityCompacting || testSession(model).compactions != 1 {
		t.Fatalf("running=%v activity=%+v compactions=%d", model.running, model.activity, testSession(model).compactions)
	}
	fake := testSession(model)
	for _, update := range []session.Update{
		fake.event(agent.Event{Kind: agent.EventCompactionStart}),
		fake.event(agent.Event{Kind: agent.EventCompactionEnd, Usage: agent.Usage{TotalTokens: 100}}),
		fake.settle(nil),
	} {
		if _, err := controller.transition(ctx, sessionEvent(update)); err != nil {
			t.Fatal(err)
		}
	}
	if model.running || model.activity.kind != activityReady || model.contextTokens != 0 {
		t.Fatalf("running=%v activity=%+v context=%d", model.running, model.activity, model.contextTokens)
	}
	if len(model.entries) != 2 || model.entries[0].Text != "existing conversation" || model.entries[1].Kind != session.EntryContext || model.entries[1].Text != "Context compacted" {
		t.Fatalf("blocks = %+v", model.entries)
	}
}

func TestTUIControllerSetsShowsAndClearsGoal(t *testing.T) {
	engine := &fakeEngine{}
	model := newTestModel(80, 24, Options{})
	controller := newTestController(model)
	controller.controls = controlsFor(engine)
	ctx := context.Background()

	if _, err := controller.applyAction(ctx, commandAction("/goal", "")); err != nil {
		t.Fatal(err)
	}
	if len(model.entries) != 1 || model.entries[0].Kind != session.EntryInfo {
		t.Fatalf("blocks = %+v", model.entries)
	}

	if _, err := controller.applyAction(ctx, commandAction("/goal", "finish migration")); err != nil {
		t.Fatal(err)
	}
	goal, ok := engine.Goal()
	if !ok || goal.Objective != "finish migration" || !model.running {
		t.Fatalf("goal=%+v exists=%v running=%v", goal, ok, model.running)
	}
	if sent := contentTexts(testSession(model).sent); !slices.Equal(sent, []string{"finish migration"}) {
		t.Fatalf("sent = %q", sent)
	}
	if _, err := controller.transition(ctx, sessionEvent(testSession(model).settle(nil))); err != nil {
		t.Fatal(err)
	}

	if _, err := controller.applyAction(ctx, commandAction("/goal", "")); err != nil {
		t.Fatal(err)
	}
	if model.entries[len(model.entries)-1].Kind != session.EntryInfo || !strings.Contains(model.entries[len(model.entries)-1].Text, "finish migration") {
		t.Fatalf("goal status block = %+v", model.entries[len(model.entries)-1])
	}

	if _, err := controller.applyAction(ctx, commandAction("/goal", "clear")); err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.Goal(); ok || model.entries[len(model.entries)-1].Kind != session.EntryInfo {
		t.Fatalf("goal still set or wrong block: %+v", model.entries[len(model.entries)-1])
	}
}

func TestTUIControllerShowsHelpAndGoalWhileRunning(t *testing.T) {
	engine := &fakeEngine{goal: &agent.GoalState{Objective: "finish migration"}}
	model := newTestModel(80, 24, Options{})
	setRunning(model)
	saveCalls := 0
	onSave(model, func(session.Draft) error {
		saveCalls++
		return nil
	})
	controller := newTestController(model)
	controller.controls = controlsFor(engine)

	for _, command := range []string{"/help", "/goal"} {
		if err := model.insertInput(command); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.Enter}}); err != nil {
			t.Fatal(err)
		}
	}
	if !model.running || saveCalls != 0 || len(model.entries) != 2 {
		t.Fatalf("running=%v saves=%d blocks=%+v", model.running, saveCalls, model.entries)
	}
	if model.entries[0].Kind != session.EntryInfo || model.entries[1].Kind != session.EntryInfo || !strings.Contains(model.entries[1].Text, "finish migration") {
		t.Fatalf("blocks=%+v", model.entries)
	}
}

func TestTUIControllerSetsGoalWhileRunning(t *testing.T) {
	engine := &fakeEngine{}
	model := newTestModel(80, 24, Options{})
	setRunning(model)
	if err := model.insertInput("/goal finish migration"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(engine), output: io.Discard,
		stopped: make(chan struct{}),
	}

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.Enter}}); err != nil {
		t.Fatal(err)
	}
	goal, ok := engine.Goal()
	last := model.entries[len(model.entries)-1]
	if !ok || goal.Objective != "finish migration" || !model.running || last.Kind != session.EntryInfo || !strings.Contains(last.Text, "finish migration") {
		t.Fatalf("goal=%+v exists=%v running=%v block=%+v", goal, ok, model.running, last)
	}
	if sent := testSession(model).sent; len(sent) != 0 {
		t.Fatalf("goal update started turns: %+v", sent)
	}
}

func TestTUIControllerClearsGoalWhileRunning(t *testing.T) {
	engine := &fakeEngine{goal: &agent.GoalState{Objective: "finish migration"}}
	model := newTestModel(80, 24, Options{})
	setRunning(model)
	if err := model.insertInput("/goal clear"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(engine), output: io.Discard,
		stopped: make(chan struct{}),
	}

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.Enter}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.Goal(); ok {
		t.Fatal("goal survived running clear command")
	}
	last := model.entries[len(model.entries)-1]
	if !model.running || last.Kind != session.EntryInfo {
		t.Fatalf("running=%v block=%+v", model.running, last)
	}
}

func TestTUIControllerDoesNotStartGoalWhenSetFails(t *testing.T) {
	setErr := errors.New("invalid goal")
	engine := &fakeEngine{setGoalErr: setErr}
	model := newTestModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(engine), output: io.Discard,
		stopped: make(chan struct{}),
	}

	if _, err := controller.applyAction(context.Background(), commandAction("/goal", "goal")); err != nil {
		t.Fatal(err)
	}
	sent := testSession(model).sent
	if len(sent) != 0 || model.running || model.activity.kind != activityError || model.activity.detail != setErr.Error() {
		t.Fatalf("sent=%+v running=%v activity=%+v", sent, model.running, model.activity)
	}
}

func TestTUIControllerNewSessionLeavesCurrentGoal(t *testing.T) {
	engine := &fakeEngine{goal: &agent.GoalState{Objective: "goal"}}
	model := newTestModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(engine), output: io.Discard,
		stopped: make(chan struct{}),
	}

	exit, err := controller.applyAction(context.Background(), commandAction("/new", ""))
	if err != nil || !exit || controller.outcome.Action != RunNewSession {
		t.Fatalf("new session exit=%v outcome=%+v error=%v", exit, controller.outcome, err)
	}
	if _, ok := engine.Goal(); !ok {
		t.Fatal("current goal was cleared")
	}
}

func TestTUIControllerAppliesSubagentStatus(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	controller := tuiController{model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard}

	_, err := controller.transition(context.Background(), sessionEvent(session.Update{
		Kind: session.UpdateSubagents,
		Subagents: subagent.Status{
			Active: []subagent.JobStatus{
				{ID: "subagent-1\nignored", Description: "inspect\nignored", State: subagent.StateRunning},
			},
			PendingCompletions: []subagent.Completion{
				{SubagentID: "subagent-2", Description: "finished", Status: subagent.StateComplete},
				{SubagentID: "subagent-3", Description: "failed", Status: subagent.StateFailed},
			},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(model.subagentStatus.PendingCompletions) != 2 || len(model.subagentStatus.Active) != 1 || model.subagentStatus.Active[0].ID != "subagent-1 ignored" || model.subagentStatus.Active[0].Description != "inspect ignored" || model.subagentStatus.PendingCompletions[0].Status != subagent.StateComplete || model.subagentStatus.PendingCompletions[1].Status != subagent.StateFailed || !controller.dirty {
		t.Fatalf("status=%+v dirty=%v", model.subagentStatus, controller.dirty)
	}

	_, err = controller.transition(context.Background(), sessionEvent(session.Update{Kind: session.UpdateSubagents}))
	if err != nil || len(model.subagentStatus.PendingCompletions) != 0 || len(model.subagentStatus.Active) != 0 {
		t.Fatalf("sanitized status=%+v error=%v", model.subagentStatus, err)
	}
}

func TestTUIControllerStoresProviderUsage(t *testing.T) {
	monthlyUsage := 12.34
	limitRemaining := 87.66
	model := newTestModel(80, 24, Options{})
	controller := tuiController{model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard}

	_, err := controller.transition(context.Background(), tuiEvent{
		kind: tuiEventProviderUsage,
		providerUsage: providerUsageMessage{usage: backend.AccountUsage{
			MonthlyUsageUSD:   &monthlyUsage,
			LimitRemainingUSD: &limitRemaining,
		}},
	})
	if err != nil || model.providerUsage.MonthlyUsageUSD == nil || *model.providerUsage.MonthlyUsageUSD != monthlyUsage || model.providerUsage.LimitRemainingUSD == nil || *model.providerUsage.LimitRemainingUSD != limitRemaining {
		t.Fatalf("provider usage = %+v, error = %v", model.providerUsage, err)
	}
}

func TestTUIControllerUsageClockRefreshesWhileRunning(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	results := make(chan providerUsageMessage, 1)
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, usageResults: results,
		loadUsage: func(context.Context) (backend.AccountUsage, error) { return backend.AccountUsage{}, nil },
	}

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventUsageClock}); err != nil {
		t.Fatal(err)
	}
	if controller.usageLoading {
		t.Fatal("idle usage refresh was requested")
	}

	setRunning(model)
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventUsageClock}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-results:
	case <-time.After(2 * time.Second):
		t.Fatal("active usage refresh was not requested")
	}
	controller.usageLoads.Wait()
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
			event:     tuiEvent{kind: tuiEventFileSearch, fileSearch: testFileSearchResult(1, "ignored")},
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
				model.subagentStatus = subagent.Status{Active: []subagent.JobStatus{{State: subagent.StateRunning}}}
			},
			event:     tuiEvent{kind: tuiEventSpinner},
			wantDirty: true,
		},
		{
			name: "completed subagent spinner is ignored",
			prepare: func(model *tuiModel) {
				model.subagentStatus = subagent.Status{PendingCompletions: []subagent.Completion{{Status: subagent.StateComplete}}}
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
				model.providerUsage.Windows = []backend.UsageWindow{{ResetsAt: time.Unix(1, 0)}}
			},
			event:     tuiEvent{kind: tuiEventUsageClock},
			wantDirty: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newTestModel(80, 24, Options{})
			if test.prepare != nil {
				test.prepare(model)
			}
			controller := tuiController{model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard}

			exit, err := controller.transition(context.Background(), test.event)
			if err != nil || exit || controller.dirty != test.wantDirty {
				t.Fatalf("exit=%v error=%v dirty=%v, want %v", exit, err, controller.dirty, test.wantDirty)
			}
		})
	}
}

func TestRenderFailureDoesNotCommitUnseenFrame(t *testing.T) {
	model := newTestModel(20, 8, Options{})
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
	model := newTestModel(20, 8, Options{})
	for range 10 {
		model.appendEntry(session.EntryInfo, "line")
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
	model := newTestModel(20, 10, Options{})
	model.appendEntry(session.EntryAssistant, "alpha")
	renderer := &tuiRenderer{}
	_ = renderModel(renderer, model)
	committed := renderer.frame

	model.session = newFakeSession()
	model.entries = nil
	model.appendEntry(session.EntryAssistant, "new unseen text")
	reduceMouse(model, keys.Mouse{Kind: keys.MousePress, Column: 1, Row: 1}, committed)
	action := reduceMouse(model, keys.Mouse{Kind: keys.MouseRelease, Column: 5, Row: 1}, committed)
	if action.kind != tuiActionCopy || action.text != "alpha" || strings.Contains(action.text, "unseen") {
		t.Fatalf("action = %+v", action)
	}
}

func TestMouseWheelUsesCommittedConversationBounds(t *testing.T) {
	model := newTestModel(20, 8, Options{})
	for range 8 {
		model.appendEntry(session.EntryInfo, "visible line")
	}
	renderer := &tuiRenderer{}
	_ = renderModel(renderer, model)
	committedTop := renderer.frame.conversationTop
	if committedTop < mouseWheelScrollLines {
		t.Fatalf("committed top = %d", committedTop)
	}
	for range 8 {
		model.appendEntry(session.EntryInfo, "unseen line")
	}

	reduceMouse(model, keys.Mouse{Kind: keys.MouseWheelUp}, renderer.frame)
	if model.scrollTop != committedTop-mouseWheelScrollLines {
		t.Fatalf("scroll top = %d, want %d", model.scrollTop, committedTop-mouseWheelScrollLines)
	}
}

func TestTUIControllerQueuesAndDequeuesPrompts(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	setRunning(model)
	controller := newTestController(model)

	if err := model.insertInput("steer"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.Enter}}); err != nil {
		t.Fatal(err)
	}
	if queued := contentTexts(model.queued); !slices.Equal(queued, []string{"steer"}) || len(testSession(model).sent) != 0 {
		t.Fatalf("queued=%q sent=%+v", queued, testSession(model).sent)
	}

	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.AltUp}}); err != nil {
		t.Fatal(err)
	}
	if len(testSession(model).queue) != 0 || len(model.queued) != 0 || model.inputText() != "steer\n\ndraft" {
		t.Fatalf("session queue=%q model queue=%q input=%q", contentTexts(testSession(model).queue), contentTexts(model.queued), model.inputText())
	}
}

func TestTUIControllerCancelRestoresQueuedPrompts(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	setRunning(model)
	testSession(model).queue = [][]agent.ContentPart{testTextContent("accepted"), testTextContent("deferred")}
	model.syncQueue()
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	controller := newTestController(model)

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.Escape}}); err != nil {
		t.Fatal(err)
	}
	if !testSession(model).interrupted || !model.interrupted || model.activity.kind != activityCanceling || len(model.queued) != 0 {
		t.Fatalf("interrupted=%v/%v activity=%+v queued=%+v", testSession(model).interrupted, model.interrupted, model.activity, model.queued)
	}
	if model.inputText() != "accepted\n\ndeferred\n\ndraft" {
		t.Fatalf("restored input = %q", model.inputText())
	}
}

func TestTUIControllerRestoresQueuedPromptsAfterRunError(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	setRunning(model)
	testSession(model).queue = [][]agent.ContentPart{testTextContent("retry this")}
	model.syncQueue()
	controller := newTestController(model)

	failure := errors.New("failed")
	if _, err := controller.transition(context.Background(), sessionEvent(testSession(model).settle(failure))); err != nil {
		t.Fatal(err)
	}
	if model.inputText() != "retry this" || len(model.queued) != 0 || model.running || model.activity.kind != activityError {
		t.Fatalf("input=%q queued=%+v running=%v activity=%+v", model.inputText(), model.queued, model.running, model.activity)
	}
}

func TestTUIControllerTogglesFastMode(t *testing.T) {
	var configured []bool
	model := newTestModel(80, 24, Options{Config: Config{FastModeAvailable: true}, Controls: Controls{SetFastMode: func(bool) {}}})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, output: io.Discard,
		stopped: make(chan struct{}),
		controls: Controls{SetFastMode: func(enabled bool) {
			configured = append(configured, enabled)
		}},
	}
	for _, want := range []bool{true, false} {
		if _, err := controller.applyAction(context.Background(), commandAction("/fast", "")); err != nil {
			t.Fatal(err)
		}
		if model.fastMode != want {
			t.Fatalf("fast mode = %v, want %v", model.fastMode, want)
		}
	}
	if !slices.Equal(configured, []bool{true, false}) {
		t.Fatalf("configured=%v", configured)
	}
	if len(model.entries) != 2 || model.entries[0].Kind != session.EntryInfo || model.entries[1].Kind != session.EntryInfo {
		t.Fatalf("blocks = %+v", model.entries)
	}
}

func TestTUIControllerTogglesFastModeWhileRunning(t *testing.T) {
	var configured bool
	model := newTestModel(80, 24, Options{Config: Config{FastModeAvailable: true}, Controls: Controls{SetFastMode: func(bool) {}}})
	setRunning(model)
	onSave(model, func(session.Draft) error {
		t.Fatal("draft saved while running")
		return nil
	})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, output: io.Discard,
		stopped: make(chan struct{}),
		controls: Controls{SetFastMode: func(enabled bool) {
			configured = enabled
		}},
	}
	if _, err := controller.applyAction(context.Background(), commandAction("/fast", "")); err != nil {
		t.Fatal(err)
	}
	if !configured || !model.fastMode || !model.running {
		t.Fatalf("configured=%v fast=%v running=%v", configured, model.fastMode, model.running)
	}
}

func TestTUIControllerAppliesThinkingLevelWhileRunning(t *testing.T) {
	var configured agent.ThinkingLevel
	saveCalls := 0
	model := newTestModel(80, 24, Options{Controls: Controls{SetThinkingLevel: func(agent.ThinkingLevel) error { return nil }}})
	setRunning(model)
	model.activity = activity{kind: activityThinking}
	onSave(model, func(session.Draft) error {
		saveCalls++
		return nil
	})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, output: io.Discard,
		stopped: make(chan struct{}),
		controls: Controls{SetThinkingLevel: func(level agent.ThinkingLevel) error {
			configured = level
			return nil
		}},
	}
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.ShiftTab}}); err != nil {
		t.Fatal(err)
	}
	if configured != agent.ThinkingHigh || model.thinkingLevel != agent.ThinkingHigh || !model.running || model.activity.kind != activityThinking || saveCalls != 0 {
		t.Fatalf("configured=%q model=%q running=%v activity=%+v saves=%d", configured, model.thinkingLevel, model.running, model.activity, saveCalls)
	}
}

func TestTUIControllerAppliesThinkingLevelOutsideModel(t *testing.T) {
	var configured agent.ThinkingLevel
	model := newTestModel(80, 24, Options{Controls: Controls{SetThinkingLevel: func(agent.ThinkingLevel) error { return nil }}})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, output: io.Discard,
		stopped: make(chan struct{}),
		controls: Controls{SetThinkingLevel: func(level agent.ThinkingLevel) error {
			configured = level
			return nil
		}},
	}
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.ShiftTab}}); err != nil {
		t.Fatal(err)
	}
	if configured != agent.ThinkingHigh || model.thinkingLevel != agent.ThinkingHigh {
		t.Fatalf("configured=%q model=%q", configured, model.thinkingLevel)
	}
}
