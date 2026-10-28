package terminal

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
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
	session                   Session
	entries                   []session.Entry
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
	queued      [][]agent.ContentPart
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
	providerUsage              backend.AccountUsage
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
			session:   options.Session,
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
	model.syncConversation()
	model.restoreDraft(options.Config.Draft)
	return model
}

func (m *tuiModel) syncConversation() {
	entries, changed := m.session.Sync(m.entries)
	if !changed {
		return
	}
	m.entries = entries
	m.conversationChanged()
}

func (m *tuiModel) appendEntry(kind session.EntryKind, text string) {
	m.session.Append(kind, text)
	m.syncConversation()
}

func (m *tuiModel) syncQueue() {
	m.queued = m.session.Queued()
	m.conversationChanged()
}

func (m *tuiModel) conversationChanged() {
	m.conversationVersion++
}

func (m *tuiModel) applyAgentEvent(event agent.Event) {
	m.syncConversation()

	switch event.Kind {
	case agent.EventSteering:
		m.syncQueue()
		m.setActiveActivity(activity{kind: activityThinking})
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
	for index := len(m.entries) - 1; index >= 0; index-- {
		if m.entries[index].Kind == session.EntryToolPending {
			return toolActivity(m.entries[index].ToolName)
		}
	}
	return activity{kind: activityThinking}
}

func (m *tuiModel) setActiveActivity(next activity) {
	if m.activity.kind != activityCanceling {
		m.activity = next
	}
}

// restoreQueued puts prompts taken from the session's queue back into the
// editor, ahead of the current input.
func (m *tuiModel) restoreQueued(messages [][]agent.ContentPart) {
	m.syncQueue()
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

func (m *tuiModel) beginTurn(kind activityKind) {
	m.running = true
	m.interrupted = false
	m.activity = activity{kind: kind}
	m.refreshCommandPickerAvailability()
	m.syncConversation()
}

// finishTurn follows the session, which may already run a turn sent after
// the one that settled.
func (m *tuiModel) finishTurn(runErr error) {
	m.running = m.session.Running()
	m.interrupted = false
	m.refreshCommandPickerAvailability()
	m.syncConversation()

	switch {
	case m.running:
		m.activity = activity{kind: activityThinking}
	case runErr != nil:
		m.activity = activity{kind: activityError, detail: singleLine(runErr.Error(), 500)}
	default:
		m.activity = activity{kind: activityReady}
	}
}
