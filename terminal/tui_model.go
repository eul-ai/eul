package terminal

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"yaah/agent"
)

type blockKind uint8

const (
	blockUser blockKind = iota
	blockAssistant
	blockReasoning
	blockToolPending
	blockTool
	blockToolError
	blockContext
	blockError
	blockInfo
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
	activityCompacting
	activityTool
	activityCanceling
	activityError
)

type activity struct {
	kind   activityKind
	detail string
}

type tuiModel struct {
	width             int
	height            int
	model             string
	thinkingLevel     agent.ThinkingLevel
	thinkingLevels    []agent.ThinkingLevel
	setThinkingLevel  func(agent.ThinkingLevel) error
	contextWindow     int64
	contextTokens     int64
	blocks            []conversationBlock
	conversationLines []styledLine
	wrappedWidth      int
	conversationDirty bool
	forceRedraw       bool
	streamKind        blockKind
	streamOpen        bool
	input             []rune
	cursor            int
	history           []string
	historyIndex      int
	historyDraft      string
	scrollTop         int
	following         bool
	running           bool
	interrupted       bool
	activity          activity
	spinner           int
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

	return &tuiModel{
		width:             width,
		height:            height,
		model:             singleLine(options.Model, 120),
		thinkingLevel:     agent.ThinkingLevel(singleLine(string(thinkingLevel), 40)),
		thinkingLevels:    thinkingLevels,
		setThinkingLevel:  options.SetThinkingLevel,
		contextWindow:     options.ContextWindow,
		historyIndex:      -1,
		following:         true,
		conversationDirty: true,
		activity:          activity{kind: activityReady},
	}
}

func (m *tuiModel) appendStream(kind blockKind, text string) {
	text = sanitizeAssistantText(text)
	if text == "" {
		return
	}

	if m.streamOpen && m.streamKind == kind && len(m.blocks) > 0 {
		m.blocks[len(m.blocks)-1].text += text
		m.conversationDirty = true
		return
	}

	m.closeStream()
	m.blocks = append(m.blocks, conversationBlock{kind: kind, text: text})
	m.conversationDirty = true
	m.streamKind = kind
	m.streamOpen = true
}

func (m *tuiModel) appendBlock(kind blockKind, text string) {
	m.closeStream()
	m.blocks = append(m.blocks, conversationBlock{kind: kind, text: sanitizeAssistantText(text)})
	m.conversationDirty = true
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
		m.setActiveActivity(activity{kind: activityThinking})
	case agent.EventContextUsage:
		m.contextTokens = event.Usage.TotalTokens
	case agent.EventToolStart:
		m.startTool(event.Call, event.Presentation)
	case agent.EventToolUpdate:
		m.updateTool(event.Call, event.Presentation)
	case agent.EventToolExecute:
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
	m.conversationDirty = true
	m.setActiveActivity(activity{kind: activityTool, detail: toolActivityDetail(call, m.blocks[len(m.blocks)-1].tool)})
}

func (m *tuiModel) updateTool(call agent.ToolCall, presentation agent.ToolPresentation) {
	index := m.toolBlockIndex(call.ID)
	if index < 0 {
		m.startTool(call, presentation)
		return
	}
	m.blocks[index].tool = sanitizeToolPresentation(call, presentation)
	m.conversationDirty = true
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
	m.conversationDirty = true
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
	return nil
}

func (m *tuiModel) insertNewline() error {
	if len(string(m.input))+1 > maxInputBytes {
		return errInputTooLong
	}
	m.insertRunes([]rune{'\n'})
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
}

func (m *tuiModel) cycleThinkingLevel() error {
	if m.setThinkingLevel == nil || len(m.thinkingLevels) == 0 {
		return errors.New("thinking level selection is unavailable")
	}

	next := m.thinkingLevels[0]
	for index, level := range m.thinkingLevels {
		if level == m.thinkingLevel {
			next = m.thinkingLevels[(index+1)%len(m.thinkingLevels)]
			break
		}
	}
	if err := m.setThinkingLevel(next); err != nil {
		return err
	}
	m.thinkingLevel = next
	return nil
}

func (m *tuiModel) backspace() {
	if m.cursor == 0 {
		return
	}

	m.leaveHistory()
	copy(m.input[m.cursor-1:], m.input[m.cursor:])
	m.input = m.input[:len(m.input)-1]
	m.cursor--
}

func (m *tuiModel) delete() {
	if m.cursor >= len(m.input) {
		return
	}

	m.leaveHistory()
	copy(m.input[m.cursor:], m.input[m.cursor+1:])
	m.input = m.input[:len(m.input)-1]
}

func (m *tuiModel) moveLeft() {
	if m.cursor > 0 {
		m.cursor--
	}
}

func (m *tuiModel) moveRight() {
	if m.cursor < len(m.input) {
		m.cursor++
	}
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

func (m *tuiModel) clearConversation() {
	m.blocks = nil
	m.conversationLines = nil
	m.conversationDirty = true
	m.closeStream()
	m.contextTokens = 0
	m.scrollTop = 0
	m.following = true
	m.activity = activity{kind: activityReady}
}

func (m *tuiModel) finishPendingTools(outcome string) {
	for index := range m.blocks {
		if m.blocks[index].kind != blockToolPending {
			continue
		}
		m.blocks[index].kind = blockToolError
		m.blocks[index].toolOutcome = outcome
		m.conversationDirty = true
	}
}

func (m *tuiModel) beginTurn(prompt string) {
	m.appendBlock(blockUser, prompt)
	m.running = true
	m.interrupted = false
	m.activity = activity{kind: activityThinking}
}

func (m *tuiModel) finishTurn(runErr error, engine Engine) {
	m.running = false
	m.closeStream()

	if m.interrupted {
		m.finishPendingTools("canceled")
		cleared := resetIfNeeded(engine)
		message := "Interrupted"
		if cleared {
			m.contextTokens = 0
			message = "Interrupted; conversation cleared after incomplete tool turn"
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
	m.activity = activity{kind: activityError, detail: detail}
	if resetIfNeeded(engine) {
		m.contextTokens = 0
		m.appendBlock(blockInfo, "Conversation cleared after incomplete tool turn")
	}
}
