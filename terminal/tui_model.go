package terminal

import (
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
	kind blockKind
	text string
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
	effort            string
	contextWindow     int64
	contextTokens     int64
	blocks            []conversationBlock
	conversationLines []styledLine
	wrappedWidth      int
	conversationDirty bool
	streamKind        blockKind
	streamOpen        bool
	activeTool        int
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
	effort := options.Effort
	if effort == "" {
		effort = "default"
	}

	return &tuiModel{
		width:             width,
		height:            height,
		model:             singleLine(options.Model, 120),
		effort:            singleLine(effort, 40),
		contextWindow:     options.ContextWindow,
		historyIndex:      -1,
		activeTool:        -1,
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
		detail := summarizeCall(event.Call)
		m.appendBlock(blockToolPending, detail)
		m.activeTool = len(m.blocks) - 1
		m.setActiveActivity(activity{kind: activityTool, detail: detail})
	case agent.EventToolEnd:
		m.finishTool(event.Result)
		m.setActiveActivity(activity{kind: activityThinking})
	}
}

func (m *tuiModel) finishTool(result agent.ToolResult) {
	kind := blockTool
	if result.IsError {
		kind = blockToolError
	}
	if m.activeTool < 0 || m.activeTool >= len(m.blocks) {
		m.activeTool = -1
		m.appendBlock(kind, summarizeResult(result))
		return
	}

	summary := summarizeResult(result)
	outcome := strings.TrimPrefix(summary, diagnostic(result.Tool, 200)+" — ")
	m.blocks[m.activeTool].kind = kind
	m.blocks[m.activeTool].text += " — " + outcome
	m.activeTool = -1
	m.conversationDirty = true
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

	text = strings.Map(func(character rune) rune {
		switch character {
		case '\n', '\r', '\t':
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

	m.leaveHistory()
	inserted := []rune(text)
	m.input = append(m.input, inserted...)
	copy(m.input[m.cursor+len(inserted):], m.input[m.cursor:len(m.input)-len(inserted)])
	copy(m.input[m.cursor:], inserted)
	m.cursor += len(inserted)
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
	m.input = nil
	m.cursor = 0
	m.historyIndex = -1
	m.historyDraft = ""
	return prompt, true
}

func (m *tuiModel) clearConversation() {
	m.blocks = nil
	m.conversationLines = nil
	m.conversationDirty = true
	m.activeTool = -1
	m.closeStream()
	m.contextTokens = 0
	m.scrollTop = 0
	m.following = true
	m.activity = activity{kind: activityReady}
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

	detail := diagnostic(runErr.Error(), 500)
	m.appendBlock(blockError, detail)
	m.activity = activity{kind: activityError, detail: detail}
	if resetIfNeeded(engine) {
		m.contextTokens = 0
		m.appendBlock(blockInfo, "Conversation cleared after incomplete tool turn")
	}
}
