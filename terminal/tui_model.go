package terminal

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
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
	toolCallID  string
	tool        agent.ToolPresentation
	toolOutcome string
}

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

type conversationModel struct {
	blocks              []conversationBlock
	conversationVersion uint64
	streamKind          blockKind
	streamOpen          bool
	scrollTop           int
	following           bool
	selection           textSelection
}

type editorModel struct {
	input              []rune
	cursor             int
	commandCompletions []commandCompletion
	commandPicker      commandPickerState
	filePicker         filePickerState
	resumePicker       resumePickerState
	history            []string
	historyIndex       int
	historyDraft       string
}

type operationModel struct {
	steering         []string
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
	contextWindow              int64
	contextTokens              int64
	providerUsage              agent.ProviderUsage
	subagentStatus             agent.SubagentStatus
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
	thinkingLevel := options.ThinkingLevel
	if thinkingLevel == "" {
		thinkingLevel = agent.DefaultThinkingLevel
	}
	thinkingLevels := append([]agent.ThinkingLevel(nil), options.ThinkingLevels...)
	if len(thinkingLevels) == 0 {
		thinkingLevels = agent.ThinkingLevels()
	}

	model := &tuiModel{
		conversationModel: conversationModel{
			following: true,
		},
		editorModel: editorModel{
			commandCompletions: commandCompletions(options.Skills),
			filePicker:         filePickerState{enabled: options.WorkingDirectory != ""},
			historyIndex:       -1,
		},
		statusModel: statusModel{
			width:                      width,
			height:                     height,
			model:                      singleLine(options.Model, 120),
			sessionID:                  singleLine(options.SessionID, 120),
			thinkingLevel:              agent.ThinkingLevel(singleLine(string(thinkingLevel), 40)),
			thinkingLevels:             thinkingLevels,
			thinkingSelectionAvailable: options.SetThinkingLevel != nil,
			contextWindow:              options.ContextWindow,
			activity:                   activity{kind: activityReady},
		},
	}
	if options.InitialCheckpoint != nil {
		restoreModelCheckpoint(model, *options.InitialCheckpoint)
	}
	if options.PreviousTurnActive {
		model.appendBlock(blockInfo, "Previous session ended during an active turn; tool side effects may remain")
	}
	for _, warning := range options.Warnings {
		model.appendBlock(blockInfo, warning)
	}
	return model
}

func (m *tuiModel) appendStream(kind blockKind, text string) {
	text = sanitizeAssistantText(text)
	if text == "" {
		return
	}

	if m.streamOpen && m.streamKind == kind && len(m.blocks) > 0 {
		m.blocks[len(m.blocks)-1].text += text
		m.conversationVersion++
		return
	}

	m.closeStream()
	m.blocks = append(m.blocks, conversationBlock{kind: kind, text: text})
	m.conversationVersion++
	m.streamKind = kind
	m.streamOpen = true
}

func (m *tuiModel) appendBlock(kind blockKind, text string) {
	m.closeStream()
	m.blocks = append(m.blocks, conversationBlock{kind: kind, text: sanitizeAssistantText(text)})
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
		m.appendBlock(blockContext, "Compacting conversation")
		m.setActiveActivity(activity{kind: activityCompacting})
	case agent.EventCompactionEnd:
		m.contextTokens = 0
		m.setActiveActivity(activity{kind: activityThinking})
	case agent.EventContextUsage:
		m.contextTokens = event.Usage.TotalTokens
	case agent.EventGenerationRetry:
		m.setActiveActivity(activity{kind: activityRetrying, detail: "attempt " + strconv.Itoa(event.Attempt)})
	case agent.EventSteering:
		m.deliverSteering(event.Text)
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
		tool:       sanitizeToolPresentation(call, presentation),
	})
	m.conversationVersion++
	m.setActiveActivity(activity{kind: activityTool, detail: toolActivityDetail(call, m.blocks[len(m.blocks)-1].tool)})
}

func (m *tuiModel) updateTool(call agent.ToolCall, presentation agent.ToolPresentation) {
	index := m.toolBlockIndex(call.ID)
	if index < 0 {
		m.startTool(call, presentation)
		return
	}
	m.blocks[index].tool = sanitizeToolPresentation(call, presentation)
	m.conversationVersion++
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
	block.tool = sanitizeToolPresentation(call, presentation)
	block.toolOutcome = sanitizeAssistantText(toolResultOutcome(result, presentation))
	m.conversationVersion++
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
			return toolActivityDetail(agent.ToolCall{}, m.blocks[index].tool), true
		}
	}
	return "", false
}

func (m *tuiModel) setActiveActivity(next activity) {
	if m.activity.kind != activityCanceling {
		m.activity = next
	}
}

func (m *tuiModel) insertInput(text string) error {
	if !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
		return errInvalidInput
	}

	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.Map(func(character rune) rune {
		switch character {
		case '\n':
			return '\n'
		case '\t':
			return ' '
		}
		if unicode.IsControl(character) {
			return '�'
		}
		return character
	}, text)
	if len(string(m.input))+len(text) > maxInputBytes {
		return errInputTooLong
	}

	m.insertRunes([]rune(text))
	m.refreshInputPickers(true)
	return nil
}

func (m *tuiModel) insertNewline() error {
	if len(string(m.input))+1 > maxInputBytes {
		return errInputTooLong
	}
	m.insertRunes([]rune{'\n'})
	m.clearInputPickers()
	return nil
}

func (m *tuiModel) insertRunes(inserted []rune) {
	m.leaveHistory()
	m.input = append(m.input, inserted...)
	copy(m.input[m.cursor+len(inserted):], m.input[m.cursor:len(m.input)-len(inserted)])
	copy(m.input[m.cursor:], inserted)
	m.cursor += len(inserted)
}

func (m *tuiModel) clearInput() {
	m.input = nil
	m.cursor = 0
	m.historyIndex = -1
	m.historyDraft = ""
	m.clearInputPickers()
}

func (m *tuiModel) refreshInputPickers(reopen bool) {
	m.refreshCommandPicker(reopen)
	m.refreshFilePicker(reopen)
}

func (m *tuiModel) clearInputPickers() {
	m.clearCommandPicker()
	m.clearFilePicker()
	m.dismissResumePicker()
}

func (m *tuiModel) nextThinkingLevel() (agent.ThinkingLevel, error) {
	if !m.thinkingSelectionAvailable || len(m.thinkingLevels) == 0 {
		return "", errors.New("thinking level selection is unavailable")
	}

	next := m.thinkingLevels[0]
	for index, level := range m.thinkingLevels {
		if level == m.thinkingLevel {
			next = m.thinkingLevels[(index+1)%len(m.thinkingLevels)]
			break
		}
	}
	return next, nil
}

func (m *tuiModel) backspace() {
	if m.cursor == 0 {
		return
	}

	m.leaveHistory()
	copy(m.input[m.cursor-1:], m.input[m.cursor:])
	m.input = m.input[:len(m.input)-1]
	m.cursor--
	m.refreshInputPickers(true)
}

func (m *tuiModel) delete() {
	if m.cursor >= len(m.input) {
		return
	}

	m.leaveHistory()
	copy(m.input[m.cursor:], m.input[m.cursor+1:])
	m.input = m.input[:len(m.input)-1]
	m.refreshInputPickers(true)
}

func (m *tuiModel) moveLeft() {
	if m.cursor > 0 {
		m.cursor--
		m.refreshInputPickers(false)
	}
}

func (m *tuiModel) moveRight() {
	if m.cursor < len(m.input) {
		m.cursor++
		m.refreshInputPickers(false)
	}
}

func (m *tuiModel) moveHome() {
	for m.cursor > 0 && m.input[m.cursor-1] != '\n' {
		m.cursor--
	}
	m.refreshInputPickers(false)
}

func (m *tuiModel) moveEnd() {
	for m.cursor < len(m.input) && m.input[m.cursor] != '\n' {
		m.cursor++
	}
	m.refreshInputPickers(false)
}

func (m *tuiModel) moveUp() bool {
	lineStart := m.cursor
	for lineStart > 0 && m.input[lineStart-1] != '\n' {
		lineStart--
	}
	if lineStart == 0 {
		return false
	}

	previousLineEnd := lineStart - 1
	previousLineStart := previousLineEnd
	for previousLineStart > 0 && m.input[previousLineStart-1] != '\n' {
		previousLineStart--
	}

	column := m.cursor - lineStart
	m.cursor = min(previousLineStart+column, previousLineEnd)
	m.refreshInputPickers(false)
	return true
}

func (m *tuiModel) moveDown() bool {
	lineStart := m.cursor
	for lineStart > 0 && m.input[lineStart-1] != '\n' {
		lineStart--
	}

	lineEnd := m.cursor
	for lineEnd < len(m.input) && m.input[lineEnd] != '\n' {
		lineEnd++
	}
	if lineEnd == len(m.input) {
		return false
	}

	nextLineStart := lineEnd + 1
	nextLineEnd := nextLineStart
	for nextLineEnd < len(m.input) && m.input[nextLineEnd] != '\n' {
		nextLineEnd++
	}

	column := m.cursor - lineStart
	m.cursor = min(nextLineStart+column, nextLineEnd)
	m.refreshInputPickers(false)
	return true
}

func (m *tuiModel) historyUp() {
	if len(m.history) == 0 {
		return
	}

	if m.historyIndex < 0 {
		m.historyDraft = string(m.input)
		m.historyIndex = len(m.history) - 1
	} else if m.historyIndex > 0 {
		m.historyIndex--
	}
	m.setInput(m.history[m.historyIndex])
}

func (m *tuiModel) historyDown() {
	if m.historyIndex < 0 {
		return
	}

	if m.historyIndex < len(m.history)-1 {
		m.historyIndex++
		m.setInput(m.history[m.historyIndex])
		return
	}

	m.historyIndex = -1
	m.setInput(m.historyDraft)
}

func (m *tuiModel) leaveHistory() {
	if m.historyIndex < 0 {
		return
	}
	m.historyIndex = -1
	m.historyDraft = ""
}

func (m *tuiModel) setInput(value string) {
	m.input = []rune(value)
	m.cursor = len(m.input)
	m.clearInputPickers()
}

func (m *tuiModel) takePrompt() (string, bool) {
	prompt := string(m.input)
	if strings.TrimSpace(prompt) == "" {
		return "", false
	}

	if len(m.history) == 0 || m.history[len(m.history)-1] != prompt {
		m.history = append(m.history, prompt)
	}
	m.clearInput()
	return prompt, true
}

func (m *tuiModel) queueSteering(prompt string) {
	m.steering = append(m.steering, prompt)
	m.conversationVersion++
}

func (m *tuiModel) deliverSteering(prompt string) {
	m.removeSteering([]string{prompt})
	m.appendBlock(blockUser, prompt)
	m.setActiveActivity(activity{kind: activityThinking})
}

func (m *tuiModel) removeSteering(messages []string) {
	for _, message := range messages {
		for index, pending := range m.steering {
			if pending != message {
				continue
			}
			m.steering = append(m.steering[:index], m.steering[index+1:]...)
			m.conversationVersion++
			break
		}
	}
}

func (m *tuiModel) restoreSteering(messages []string) {
	if len(messages) == 0 {
		return
	}
	m.removeSteering(messages)
	queued := strings.Join(messages, "\n\n")
	current := string(m.input)
	if strings.TrimSpace(current) != "" {
		queued += "\n\n" + current
	}
	m.setInput(queued)
}

func (m *tuiModel) restoreAllSteering() {
	messages := append([]string(nil), m.steering...)
	m.restoreSteering(messages)
}

func (m *tuiModel) clearConversation() {
	m.blocks = nil
	m.steering = nil
	m.conversationVersion++
	m.closeStream()
	m.contextTokens = 0
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
		m.conversationVersion++
	}
}

func (m *tuiModel) beginTurn(prompt string) {
	m.appendBlock(blockUser, prompt)
	m.running = true
	m.refreshCommandPickerAvailability()
	m.interrupted = false
	m.turnExecutedTool = false
	m.activity = activity{kind: activityThinking}
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
