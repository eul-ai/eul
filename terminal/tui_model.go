package terminal

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal/text"
)

const (
	maxAttachedImages           = 10
	maxAttachedImagesTotalBytes = 10 * 1024 * 1024
)

var (
	errTooManyImages  = fmt.Errorf("a prompt can include at most %d images", maxAttachedImages)
	errImagesTooLarge = fmt.Errorf("attached images exceed %d MiB", maxAttachedImagesTotalBytes/(1024*1024))
)

const imageAttachmentLabel = "[image attached]"

type activityKind uint8

const (
	activityReady activityKind = iota
	activityThinking
	activityResponding
	activityRetrying
	activityCompacting
	activityTool
	activityCanceling
	activityError
)

type activity struct {
	kind   activityKind
	detail string
}

func (a activity) animated() bool {
	switch a.kind {
	case activityReady, activityError:
		return false
	default:
		return true
	}
}

type conversationModel struct {
	transcript                session.Transcript
	conversationVersion       uint64
	scrollTop                 int
	following                 bool
	historyExpansionRequested bool
	selection                 textSelection
}

type editorItemKind uint8

const (
	editorItemRune editorItemKind = iota
	editorItemImage
	editorItemPendingImage
)

type editorItem struct {
	kind      editorItemKind
	character rune
	image     *agent.Image
	requestID uint64
}

type editorModel struct {
	input              []editorItem
	cursor             int
	commands           []slashCommand
	commandCompletions []commandCompletion
	commandPicker      commandPickerState
	filePicker         filePickerState
	resumePicker       resumePickerState
	globalHistory      []string
	history            []string
	pendingHistory     []string
	historyIndex       int
	historyDraft       []editorItem
	historyDraftCursor int
}

const permissionDecisionCount PermissionDecision = PermissionAllowSession + 1

type permissionState struct {
	queue    []PermissionRequest
	selected PermissionDecision
	scroll   int
}

func (permission permissionState) active() bool {
	return len(permission.queue) > 0
}

type permissionView struct {
	title        string
	subject      string
	description  string
	detail       string
	detailPrefix string
	notice       string
}

func (permission permissionState) view() permissionView {
	request := permission.queue[0]
	title := singleLine(request.Title, 200)
	if strings.TrimSpace(title) == "" {
		title = "Permission requested"
	}
	if len(permission.queue) > 1 {
		title += fmt.Sprintf(" (1 of %d)", len(permission.queue))
	}
	return permissionView{
		title:        title,
		subject:      singleLine(request.Subject, 120),
		description:  singleLine(request.Description, 500),
		detail:       sanitizeAssistantText(request.Detail),
		detailPrefix: text.Truncate(replaceControls(request.DetailPrefix), 20, false),
		notice:       singleLine(request.Notice, 500),
	}
}

type operationModel struct {
	steering    steeringCoordinator
	permission  permissionState
	running     bool
	interrupted bool
}

type statusModel struct {
	width                      int
	height                     int
	model                      string
	sessionID                  string
	thinkingLevel              agent.ThinkingLevel
	thinkingLevels             []agent.ThinkingLevel
	thinkingSelectionAvailable bool
	fastMode                   bool
	fastModeAvailable          bool
	contextWindow              int64
	contextTokens              int64
	providerUsage              ProviderUsage
	subagentStatus             subagent.Status
	activity                   activity
	spinner                    int
}

type tuiModel struct {
	conversationModel
	editorModel
	operationModel
	statusModel
}

func newTUIModel(width, height int, options Options) *tuiModel {
	thinkingLevel := options.Config.ThinkingLevel
	if thinkingLevel == "" {
		thinkingLevel = agent.DefaultThinkingLevel
	}
	thinkingLevels := append([]agent.ThinkingLevel(nil), options.Config.ThinkingLevels...)
	if len(thinkingLevels) == 0 {
		thinkingLevels = agent.ThinkingLevels()
	}
	fastModeAvailable := options.Config.FastModeAvailable && options.Controls.SetFastMode != nil
	commands := availableCommands(fastModeAvailable)

	model := &tuiModel{
		conversationModel: conversationModel{
			following: true,
		},
		editorModel: editorModel{
			commands:           commands,
			commandCompletions: commandCompletions(commands, options.Config.Skills),
			filePicker:         filePickerState{enabled: options.Config.WorkingDirectory != ""},
			globalHistory:      append([]string(nil), options.MessageHistory.Entries...),
			historyIndex:       -1,
		},
		statusModel: statusModel{
			width:                      width,
			height:                     height,
			model:                      singleLine(options.Config.Model, 120),
			sessionID:                  singleLine(options.Config.SessionID, 120),
			thinkingLevel:              agent.ThinkingLevel(singleLine(string(thinkingLevel), 40)),
			thinkingLevels:             thinkingLevels,
			thinkingSelectionAvailable: options.Controls.SetThinkingLevel != nil,
			fastMode:                   options.Config.FastMode,
			fastModeAvailable:          fastModeAvailable,
			contextWindow:              options.Config.ContextWindow,
			contextTokens:              options.Config.ContextTokens,
			activity:                   activity{kind: activityReady},
		},
	}
	if options.Config.InitialCheckpoint != nil {
		restoreModelCheckpoint(model, *options.Config.InitialCheckpoint)
	}
	if options.Config.PreviousTurnActive {
		model.appendEntry(session.EntryInfo, "Previous session ended during an active turn; tool side effects may remain")
	}
	for _, warning := range options.Config.Warnings {
		model.appendEntry(session.EntryInfo, warning)
	}
	return model
}

func (m *tuiModel) pendingSteering() [][]agent.ContentPart {
	return m.steering.pending()
}

func (m *tuiModel) enqueueSteering(content []agent.ContentPart, accepted bool) {
	m.steering.enqueue(content, accepted)
	m.conversationChanged()
}

func (m *tuiModel) deliverSteering(content []agent.ContentPart) bool {
	if !m.steering.delivered(content) {
		return false
	}
	m.conversationChanged()
	return true
}

func (m *tuiModel) nextDeferredSteering() ([]agent.ContentPart, bool) {
	content, ok := m.steering.nextDeferred()
	if ok {
		m.conversationChanged()
	}
	return content, ok
}

func (m *tuiModel) restoreDeferredSteering(content []agent.ContentPart) {
	m.steering.restoreDeferred(content)
	m.conversationChanged()
}

func (m *tuiModel) clearSteering(clear func() [][]agent.ContentPart) [][]agent.ContentPart {
	messages := m.steering.restore(clear)
	if len(messages) > 0 {
		m.conversationChanged()
	}
	return messages
}

func (m *tuiModel) appendEntry(kind session.EntryKind, text string) {
	m.transcript.Append(kind, text)
	m.conversationChanged()
}

func (m *tuiModel) conversationChanged() {
	m.conversationVersion++
}

func (m *tuiModel) applyAgentEvent(event agent.Event) {
	m.transcript.Apply(event)
	m.conversationChanged()

	switch event.Kind {
	case agent.EventAssistantReasoning:
		m.setActiveActivity(activity{kind: activityThinking})
	case agent.EventAssistantText:
		m.setActiveActivity(activity{kind: activityResponding})
	case agent.EventCompactionStart:
		m.setActiveActivity(activity{kind: activityCompacting})
	case agent.EventCompactionEnd:
		m.contextTokens = 0
		m.setActiveActivity(activity{kind: activityThinking})
	case agent.EventContextUsage:
		m.contextTokens = event.Usage.TotalTokens
	case agent.EventGenerationRetry:
		m.setActiveActivity(activity{kind: activityRetrying, detail: "attempt " + strconv.Itoa(event.Attempt)})
	case agent.EventGoalContinuation:
		m.setActiveActivity(activity{kind: activityThinking})
	case agent.EventToolStart, agent.EventToolExecute:
		m.setActiveActivity(toolActivity(event.Call.Name))
	case agent.EventToolEnd:
		m.setActiveActivity(m.pendingToolActivity())
	}
}

func toolActivity(name string) activity {
	return activity{kind: activityTool, detail: singleLine(name, maxToolPresentationSummaryBytes)}
}

func (m *tuiModel) pendingToolActivity() activity {
	entries := m.transcript.Entries
	for index := len(entries) - 1; index >= 0; index-- {
		if entries[index].Kind == session.EntryToolPending {
			return toolActivity(entries[index].ToolName)
		}
	}
	return activity{kind: activityThinking}
}

func (m *tuiModel) setActiveActivity(next activity) {
	if m.activity.kind != activityCanceling {
		m.activity = next
	}
}

func (m *tuiModel) restoreSteering(messages [][]agent.ContentPart) {
	if len(messages) == 0 {
		return
	}

	var restored []editorItem
	for index, content := range messages {
		if index > 0 {
			restored = append(restored, editorItemsFromText("\n\n")...)
		}
		restored = append(restored, editorItemsFromContent(content)...)
	}
	if len(m.input) > 0 {
		restored = append(restored, editorItemsFromText("\n\n")...)
		restored = append(restored, m.input...)
	}
	m.input = restored
	m.cursor = len(m.input)
	m.leaveHistory()
	m.clearInputPickers()
}

func (m *tuiModel) appendUserContent(content []agent.ContentPart) {
	m.transcript.AppendUser(content)
	m.conversationChanged()
}

func (m *tuiModel) beginTurnOperation() {
	m.running = true
	m.refreshCommandPickerAvailability()
	m.interrupted = false
	m.activity = activity{kind: activityThinking}
}

func (m *tuiModel) rollbackTurnStart() {
	m.running = false
	m.activity = activity{kind: activityReady}
	m.refreshCommandPickerAvailability()
}

func (m *tuiModel) beginCompaction() {
	m.running = true
	m.refreshCommandPickerAvailability()
	m.interrupted = false
	m.activity = activity{kind: activityCompacting}
}

func (m *tuiModel) finishTurn(runErr error) {
	m.running = false
	m.refreshCommandPickerAvailability()
	m.transcript.FinishTurn(m.interrupted, runErr)
	m.conversationChanged()

	m.activity = activity{kind: activityReady}
	if !m.interrupted && runErr != nil {
		m.activity = activity{kind: activityError, detail: singleLine(runErr.Error(), 500)}
	}
}
