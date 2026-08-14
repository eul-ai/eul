package terminal

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
)

const (
	maxAttachedImages           = 10
	maxAttachedImagesTotalBytes = 10 * 1024 * 1024
)

var (
	errTooManyImages  = fmt.Errorf("a prompt can include at most %d images", maxAttachedImages)
	errImagesTooLarge = fmt.Errorf("attached images exceed %d MiB", maxAttachedImagesTotalBytes/(1024*1024))
)

type blockKind uint8

// Values are persisted in terminal checkpoints and must remain stable.
const (
	blockUser        blockKind = 0
	blockAssistant   blockKind = 1
	blockReasoning   blockKind = 2
	blockToolPending blockKind = 3
	blockTool        blockKind = 4
	blockToolError   blockKind = 5
	blockContext     blockKind = 6
	blockError       blockKind = 7
	blockInfo        blockKind = 8
)

type conversationBlock struct {
	kind        blockKind
	text        string
	content     []agent.ContentPart
	toolCallID  string
	toolName    string
	tool        agent.ToolPresentation
	toolOutcome string
}

const imageAttachmentLabel = "[image attached]"

type activityKind uint8

const (
	activityReady activityKind = iota
	activityThinking
	activityResponding
	activityRetrying
	activityCompacting
	activityTool
	activityPermission
	activityCanceling
	activityError
)

type activity struct {
	kind   activityKind
	detail string
}

type conversationModel struct {
	blocks              []conversationBlock
	conversationVersion uint64
	streamKind          blockKind
	streamOpen          bool
	scrollTop           int
	following           bool
	selection           textSelection
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
	commandCompletions []commandCompletion
	commandPicker      commandPickerState
	filePicker         filePickerState
	resumePicker       resumePickerState
	history            []string
	historyIndex       int
	historyDraft       []editorItem
	historyDraftCursor int
}

const permissionDecisionCount PermissionDecision = PermissionAllowSession + 1

type permissionModel struct {
	title        string
	subject      string
	description  string
	detail       string
	detailPrefix string
	notice       string
	selected     PermissionDecision
	scroll       int
	index        int
	total        int
}

func (permission permissionModel) active() bool {
	return permission.title != ""
}

type operationModel struct {
	steering         steeringCoordinator
	permission       permissionModel
	turnExecutedTool bool
	running          bool
	interrupted      bool
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

	model := &tuiModel{
		conversationModel: conversationModel{
			following: true,
		},
		editorModel: editorModel{
			commandCompletions: commandCompletions(options.Config.Skills, fastModeAvailable),
			filePicker:         filePickerState{enabled: options.Config.WorkingDirectory != ""},
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
			activity:                   activity{kind: activityReady},
		},
	}
	if options.Config.InitialCheckpoint != nil {
		restoreModelCheckpoint(model, *options.Config.InitialCheckpoint)
	}
	if options.Config.PreviousTurnActive {
		model.appendBlock(blockInfo, "Previous session ended during an active turn; tool side effects may remain")
	}
	for _, warning := range options.Config.Warnings {
		model.appendBlock(blockInfo, warning)
	}
	return model
}

func (m *tuiModel) pendingSteering() []string {
	return m.steering.pending()
}

func (m *tuiModel) enqueueSteering(prompt string, accepted bool) {
	m.steering.enqueue(prompt, accepted)
	m.conversationChanged()
}

func (m *tuiModel) deliverSteering(prompt string) bool {
	if !m.steering.delivered(prompt) {
		return false
	}
	m.conversationChanged()
	return true
}

func (m *tuiModel) nextDeferredSteering() (string, bool) {
	prompt, ok := m.steering.nextDeferred()
	if ok {
		m.conversationChanged()
	}
	return prompt, ok
}

func (m *tuiModel) restoreDeferredSteering(prompt string) {
	m.steering.restoreDeferred(prompt)
	m.conversationChanged()
}

func (m *tuiModel) clearSteering(clear func() []string) []string {
	messages := m.steering.restore(clear)
	if len(messages) > 0 {
		m.conversationChanged()
	}
	return messages
}

func (m *tuiModel) appendStream(kind blockKind, text string) {
	text = sanitizeAssistantText(text)
	if text == "" {
		return
	}

	if m.streamOpen && m.streamKind == kind && len(m.blocks) > 0 {
		m.blocks[len(m.blocks)-1].text += text
		m.conversationChanged()
		return
	}

	m.closeStream()
	m.blocks = append(m.blocks, conversationBlock{kind: kind, text: text})
	m.conversationChanged()
	m.streamKind = kind
	m.streamOpen = true
}

func (m *tuiModel) appendBlock(kind blockKind, text string) {
	m.closeStream()
	m.blocks = append(m.blocks, conversationBlock{kind: kind, text: sanitizeAssistantText(text)})
	m.conversationChanged()
}

func (m *tuiModel) conversationChanged() {
	m.conversationVersion++
}

func (m *tuiModel) closeStream() {
	m.streamOpen = false
}

func (m *tuiModel) applyAgentEvent(event agent.Event) {
	switch event.Kind {
	case agent.EventAssistantReasoning:
		m.appendStream(blockReasoning, event.Text)
		m.setActiveActivity(activity{kind: activityThinking})
	case agent.EventAssistantText:
		m.appendStream(blockAssistant, event.Text)
		m.setActiveActivity(activity{kind: activityResponding})
	case agent.EventCompactionStart:
		m.setActiveActivity(activity{kind: activityCompacting})
	case agent.EventCompactionEnd:
		m.appendBlock(blockContext, "Context compacted")
		m.contextTokens = 0
		m.setActiveActivity(activity{kind: activityThinking})
	case agent.EventContextUsage:
		m.contextTokens = event.Usage.TotalTokens
	case agent.EventGenerationRetry:
		m.setActiveActivity(activity{kind: activityRetrying, detail: "attempt " + strconv.Itoa(event.Attempt)})
	case agent.EventGoalContinuation:
		m.appendBlock(blockInfo, "Goal continuing")
		m.setActiveActivity(activity{kind: activityThinking})
	case agent.EventToolStart:
		m.startTool(event.Call, event.Presentation)
	case agent.EventToolUpdate:
		m.updateTool(event.Call, event.Presentation)
	case agent.EventToolExecute:
		m.turnExecutedTool = true
		m.updateTool(event.Call, event.Presentation)
		index := m.toolBlockIndex(event.Call.ID)
		m.setActiveActivity(activity{kind: activityTool, detail: toolActivityDetail(event.Call, m.blocks[index].tool)})
	case agent.EventToolEnd:
		m.finishTool(event.Call, event.Presentation, event.Result)
		if detail, ok := m.pendingToolActivity(); ok {
			m.setActiveActivity(activity{kind: activityTool, detail: detail})
		} else {
			m.setActiveActivity(activity{kind: activityThinking})
		}
	}
}

func (m *tuiModel) startTool(call agent.ToolCall, presentation agent.ToolPresentation) {
	m.closeStream()
	m.blocks = append(m.blocks, conversationBlock{
		kind:       blockToolPending,
		toolCallID: call.ID,
		toolName:   call.Name,
		tool:       sanitizeToolPresentation(call, presentation),
	})
	m.conversationChanged()
	m.setActiveActivity(activity{kind: activityTool, detail: toolActivityDetail(call, m.blocks[len(m.blocks)-1].tool)})
}

func (m *tuiModel) updateTool(call agent.ToolCall, presentation agent.ToolPresentation) {
	index := m.toolBlockIndex(call.ID)
	if index < 0 {
		m.startTool(call, presentation)
		return
	}
	if call.Name != "" {
		m.blocks[index].toolName = call.Name
	}
	m.blocks[index].tool = sanitizeToolPresentation(call, presentation)
	m.conversationChanged()
}

func (m *tuiModel) finishTool(call agent.ToolCall, presentation agent.ToolPresentation, result agent.ToolResult) {
	if call.Name == "" {
		call.Name = result.Tool
	}
	kind := blockTool
	if result.IsError {
		kind = blockToolError
	}
	index := m.toolBlockIndex(call.ID)
	if index < 0 {
		m.startTool(call, presentation)
		index = len(m.blocks) - 1
	}

	block := &m.blocks[index]
	block.kind = kind
	if call.Name != "" {
		block.toolName = call.Name
	}
	block.tool = sanitizeToolPresentation(call, presentation)
	block.toolOutcome = sanitizeAssistantText(toolResultOutcome(result, presentation))
	m.conversationChanged()
}

func (m *tuiModel) toolBlockIndex(callID string) int {
	for index := len(m.blocks) - 1; index >= 0; index-- {
		if m.blocks[index].toolCallID == callID && isToolBlock(m.blocks[index].kind) {
			return index
		}
	}
	return -1
}

func (m *tuiModel) pendingToolActivity() (string, bool) {
	for index := len(m.blocks) - 1; index >= 0; index-- {
		if m.blocks[index].kind == blockToolPending {
			return toolActivityDetail(agent.ToolCall{Name: m.blocks[index].toolName}, m.blocks[index].tool), true
		}
	}
	return "", false
}

func (m *tuiModel) setActiveActivity(next activity) {
	if m.activity.kind != activityCanceling {
		m.activity = next
	}
}

func (m *tuiModel) restoreSteering(messages []string) {
	if len(messages) == 0 {
		return
	}

	queued := strings.Join(messages, "\n\n")
	current := m.inputText()
	if strings.TrimSpace(current) != "" {
		queued += "\n\n" + current
	}
	m.setInput(queued)
}

func (m *tuiModel) showPermission(request PermissionRequest, index, total int) {
	title := singleLine(request.Title, 200)
	if strings.TrimSpace(title) == "" {
		title = "Permission requested"
	}
	detailPrefix := strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, request.DetailPrefix)
	m.permission = permissionModel{
		title:        title,
		subject:      singleLine(request.Subject, 120),
		description:  singleLine(request.Description, 500),
		detail:       sanitizeAssistantText(request.Detail),
		detailPrefix: truncateCells(detailPrefix, 20, false),
		notice:       singleLine(request.Notice, 500),
		index:        index,
		total:        total,
	}
	m.activity = activity{kind: activityPermission}
}

func (m *tuiModel) clearPermission() {
	m.permission = permissionModel{}
}

func (m *tuiModel) clearConversation() {
	m.blocks = nil
	m.conversationChanged()
	m.closeStream()
	m.contextTokens = 0
	m.clearPermission()
	m.turnExecutedTool = false
	m.scrollTop = 0
	m.following = true
	m.selection = textSelection{}
	m.activity = activity{kind: activityReady}
}

func (m *tuiModel) finishPendingTools(outcome string) {
	for index := range m.blocks {
		if m.blocks[index].kind != blockToolPending {
			continue
		}
		m.blocks[index].kind = blockToolError
		m.blocks[index].toolOutcome = outcome
		m.conversationChanged()
	}
}

func (m *tuiModel) beginTurn(prompt string) {
	m.beginTurnContent([]agent.ContentPart{{Kind: agent.ContentPartText, Text: prompt}})
}

func (m *tuiModel) beginTurnContent(content []agent.ContentPart) {
	m.appendUserContent(content)
	m.beginTurnOperation()
}

func (m *tuiModel) appendUserContent(content []agent.ContentPart) {
	m.closeStream()
	content = sanitizeContent(content)
	m.blocks = append(m.blocks, conversationBlock{kind: blockUser, text: contentText(content), content: content})
	m.conversationChanged()
}

func (m *tuiModel) beginTurnOperation() {
	m.running = true
	m.refreshCommandPickerAvailability()
	m.interrupted = false
	m.turnExecutedTool = false
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
	m.turnExecutedTool = false
	m.activity = activity{kind: activityCompacting}
}

func (m *tuiModel) finishTurn(runErr error) {
	m.running = false
	m.clearPermission()
	m.refreshCommandPickerAvailability()
	m.closeStream()
	executedTool := m.turnExecutedTool
	m.turnExecutedTool = false

	if m.interrupted {
		m.finishPendingTools("canceled")
		message := "Interrupted"
		if executedTool {
			message = "Interrupted; tool side effects may remain"
		}
		m.appendBlock(blockInfo, message)
		m.activity = activity{kind: activityReady}
		return
	}

	if runErr == nil {
		m.activity = activity{kind: activityReady}
		return
	}

	m.finishPendingTools("failed")
	detail := diagnostic(runErr.Error(), 500)
	m.appendBlock(blockError, detail)
	if executedTool {
		m.appendBlock(blockInfo, "Tool turn interrupted; tool side effects may remain")
	}
	m.activity = activity{kind: activityError, detail: detail}
}
