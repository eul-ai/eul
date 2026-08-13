package terminal

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
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

type permissionModel struct {
	title         string
	subject       string
	description   string
	detail        string
	detailPrefix  string
	notice        string
	allowSelected bool
	scroll        int
	index         int
	total         int
}

func (permission permissionModel) active() bool {
	return permission.title != ""
}

type operationModel struct {
	steeringView     *steeringCoordinator
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
	subagentStatus             SubagentStatus
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
	fastModeAvailable := options.Config.FastModeAvailable && canSetFastMode(options.Commands)

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
			thinkingSelectionAvailable: canSetThinkingLevel(options.Commands),
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
	if m.steeringView == nil {
		return nil
	}
	return m.steeringView.pending()
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

func editorItemsFromText(text string) []editorItem {
	items := make([]editorItem, 0, utf8.RuneCountInString(text))
	for _, character := range text {
		items = append(items, editorItem{kind: editorItemRune, character: character})
	}
	return items
}

func editorText(items []editorItem) string {
	var text strings.Builder
	for _, item := range items {
		if item.kind == editorItemRune {
			text.WriteRune(item.character)
		}
	}
	return text.String()
}

func (m *tuiModel) inputText() string {
	return editorText(m.input)
}

func (m *tuiModel) textCursor() int {
	cursor := 0
	for _, item := range m.input[:m.cursor] {
		if item.kind == editorItemRune {
			cursor++
		}
	}
	return cursor
}

func (m *tuiModel) inputReferenceRunes() []rune {
	input := make([]rune, len(m.input))
	for index, item := range m.input {
		if item.kind == editorItemRune {
			input[index] = item.character
		} else {
			input[index] = ' '
		}
	}
	return input
}

func (m *tuiModel) replaceItemRange(start, end int, replacement string) bool {
	if start < 0 || start > end || end > len(m.input) {
		return false
	}

	inserted := editorItemsFromText(replacement)
	updated := make([]editorItem, 0, len(m.input)-(end-start)+len(inserted))
	updated = append(updated, m.input[:start]...)
	updated = append(updated, inserted...)
	updated = append(updated, m.input[end:]...)
	m.input = updated
	m.cursor = start + len(inserted)
	return true
}

func (m *tuiModel) imageCount() int {
	count := 0
	for _, item := range m.input {
		if item.kind == editorItemImage {
			count++
		}
	}
	return count
}

func (m *tuiModel) imageBytes() int {
	total := 0
	for _, item := range m.input {
		if item.kind == editorItemImage && item.image != nil {
			total += len(item.image.Data)
		}
	}
	return total
}

func (m *tuiModel) submittingContent() ([]agent.ContentPart, bool) {
	content := editorContent(m.input)
	if len(content) == 0 || len(content) == 1 && content[0].Kind == agent.ContentPartText && strings.TrimSpace(content[0].Text) == "" {
		return nil, false
	}
	return content, true
}

func (m *tuiModel) finishSubmission(content []agent.ContentPart) {
	m.rememberPrompt(contentText(content))
	m.clearInput()
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
	if len(m.inputText())+len(text) > maxInputBytes {
		return errInputTooLong
	}

	m.insertRunes([]rune(text))
	m.refreshInputPickers(true)
	return nil
}

func (m *tuiModel) insertNewline() error {
	if len(m.inputText())+1 > maxInputBytes {
		return errInputTooLong
	}
	m.insertRunes([]rune{'\n'})
	m.clearInputPickers()
	return nil
}

func (m *tuiModel) insertRunes(inserted []rune) {
	m.insertItems(editorItemsFromText(string(inserted)))
}

func (m *tuiModel) insertItems(inserted []editorItem) {
	m.leaveHistory()
	m.input = append(m.input, inserted...)
	copy(m.input[m.cursor+len(inserted):], m.input[m.cursor:len(m.input)-len(inserted)])
	copy(m.input[m.cursor:], inserted)
	m.cursor += len(inserted)
}

func (m *tuiModel) clearInput() []uint64 {
	pending := m.pendingImageRequests()
	m.input = nil
	m.cursor = 0
	m.historyIndex = -1
	m.historyDraft = nil
	m.historyDraftCursor = 0
	m.clearInputPickers()
	return pending
}

func (m *tuiModel) pendingImageRequests() []uint64 {
	var requests []uint64
	for _, item := range m.input {
		if item.kind == editorItemPendingImage {
			requests = append(requests, item.requestID)
		}
	}
	return requests
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

func (m *tuiModel) backspace() uint64 {
	if m.cursor == 0 {
		return 0
	}

	m.leaveHistory()
	requestID := m.input[m.cursor-1].requestID
	copy(m.input[m.cursor-1:], m.input[m.cursor:])
	m.input = m.input[:len(m.input)-1]
	m.cursor--
	m.refreshInputPickers(true)
	return requestID
}

func (m *tuiModel) delete() uint64 {
	if m.cursor >= len(m.input) {
		return 0
	}

	m.leaveHistory()
	requestID := m.input[m.cursor].requestID
	copy(m.input[m.cursor:], m.input[m.cursor+1:])
	m.input = m.input[:len(m.input)-1]
	m.refreshInputPickers(true)
	return requestID
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

func isEditorNewline(item editorItem) bool {
	return item.kind == editorItemRune && item.character == '\n'
}

func (m *tuiModel) moveHome() {
	for m.cursor > 0 && !isEditorNewline(m.input[m.cursor-1]) {
		m.cursor--
	}
	m.refreshInputPickers(false)
}

func (m *tuiModel) moveEnd() {
	for m.cursor < len(m.input) && !isEditorNewline(m.input[m.cursor]) {
		m.cursor++
	}
	m.refreshInputPickers(false)
}

func (m *tuiModel) moveUp() bool {
	lineStart := m.cursor
	for lineStart > 0 && !isEditorNewline(m.input[lineStart-1]) {
		lineStart--
	}
	if lineStart == 0 {
		return false
	}

	previousLineEnd := lineStart - 1
	previousLineStart := previousLineEnd
	for previousLineStart > 0 && !isEditorNewline(m.input[previousLineStart-1]) {
		previousLineStart--
	}

	column := m.cursor - lineStart
	m.cursor = min(previousLineStart+column, previousLineEnd)
	m.refreshInputPickers(false)
	return true
}

func (m *tuiModel) moveDown() bool {
	lineStart := m.cursor
	for lineStart > 0 && !isEditorNewline(m.input[lineStart-1]) {
		lineStart--
	}

	lineEnd := m.cursor
	for lineEnd < len(m.input) && !isEditorNewline(m.input[lineEnd]) {
		lineEnd++
	}
	if lineEnd == len(m.input) {
		return false
	}

	nextLineStart := lineEnd + 1
	nextLineEnd := nextLineStart
	for nextLineEnd < len(m.input) && !isEditorNewline(m.input[nextLineEnd]) {
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
		m.historyDraft = make([]editorItem, 0, len(m.input))
		m.historyDraftCursor = m.cursor
		for index, item := range m.input {
			if item.kind == editorItemPendingImage {
				if index < m.cursor {
					m.historyDraftCursor--
				}
				continue
			}
			m.historyDraft = append(m.historyDraft, item)
		}
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
	m.input = m.historyDraft
	m.cursor = m.historyDraftCursor
	m.historyDraft = nil
	m.historyDraftCursor = 0
	m.clearInputPickers()
}

func (m *tuiModel) leaveHistory() {
	if m.historyIndex < 0 {
		return
	}
	m.historyIndex = -1
	m.historyDraft = nil
	m.historyDraftCursor = 0
}

func (m *tuiModel) setInput(value string) {
	m.input = editorItemsFromText(value)
	m.cursor = len(m.input)
	m.clearInputPickers()
}

func contentText(content []agent.ContentPart) string {
	var text strings.Builder
	for _, part := range content {
		if part.Kind == agent.ContentPartText {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

func cloneTerminalContent(content []agent.ContentPart) []agent.ContentPart {
	cloned := make([]agent.ContentPart, len(content))
	for index, part := range content {
		cloned[index] = part
		if part.Image != nil {
			image := *part.Image
			image.Data = append([]byte(nil), image.Data...)
			cloned[index].Image = &image
		}
	}
	return cloned
}

func sanitizeContent(content []agent.ContentPart) []agent.ContentPart {
	content = cloneTerminalContent(content)
	for index := range content {
		if content[index].Kind == agent.ContentPartText {
			content[index].Text = sanitizeAssistantText(content[index].Text)
		}
	}
	return content
}

func editorContent(items []editorItem) []agent.ContentPart {
	var content []agent.ContentPart
	var text strings.Builder
	flushText := func() {
		if text.Len() == 0 {
			return
		}
		content = append(content, agent.ContentPart{Kind: agent.ContentPartText, Text: text.String()})
		text.Reset()
	}

	for _, item := range items {
		switch item.kind {
		case editorItemRune:
			text.WriteRune(item.character)
		case editorItemImage:
			if item.image == nil {
				continue
			}
			flushText()
			image := *item.image
			image.Data = append([]byte(nil), image.Data...)
			content = append(content, agent.ContentPart{Kind: agent.ContentPartImage, Image: &image})
		}
	}
	flushText()
	return content
}

func (m *tuiModel) takePrompt() (string, bool) {
	prompt := m.inputText()
	if strings.TrimSpace(prompt) == "" {
		return "", false
	}
	m.rememberPrompt(prompt)
	m.clearInput()
	return prompt, true
}

func (m *tuiModel) rememberPrompt(prompt string) {
	if strings.TrimSpace(prompt) != "" && (len(m.history) == 0 || m.history[len(m.history)-1] != prompt) {
		m.history = append(m.history, prompt)
	}
}

func (m *tuiModel) reserveImage(requestID uint64) {
	m.insertItems([]editorItem{{kind: editorItemPendingImage, requestID: requestID}})
	m.clearInputPickers()
}

func (m *tuiModel) resolveImage(requestID uint64, image agent.Image) error {
	index := -1
	for itemIndex, item := range m.input {
		if item.kind == editorItemPendingImage && item.requestID == requestID {
			index = itemIndex
			break
		}
	}
	if index < 0 {
		return nil
	}
	if m.imageCount() >= maxAttachedImages {
		return errTooManyImages
	}
	if m.imageBytes()+len(image.Data) > maxAttachedImagesTotalBytes {
		return errImagesTooLarge
	}

	image.Data = append([]byte(nil), image.Data...)
	m.input[index] = editorItem{kind: editorItemImage, image: &image}
	m.clearInputPickers()
	m.activity = activity{kind: activityReady, detail: "image attached"}
	return nil
}

func (m *tuiModel) removePendingImage(requestID uint64) bool {
	for index := range m.input {
		if m.input[index].kind != editorItemPendingImage || m.input[index].requestID != requestID {
			continue
		}
		copy(m.input[index:], m.input[index+1:])
		m.input = m.input[:len(m.input)-1]
		if m.cursor > index {
			m.cursor--
		}
		return true
	}
	return false
}

func (m *tuiModel) attachImage(image agent.Image) error {
	const immediateRequestID = ^uint64(0)
	m.reserveImage(immediateRequestID)
	if err := m.resolveImage(immediateRequestID, image); err != nil {
		m.removePendingImage(immediateRequestID)
		return err
	}
	return nil
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
	m.conversationVersion++
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
		m.conversationVersion++
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
	m.conversationVersion++
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
