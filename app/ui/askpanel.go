package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/umputun/revdiff/app/diff"
	"github.com/umputun/revdiff/app/keymap"
	"github.com/umputun/revdiff/app/ui/style"
)

// Asker answers questions about the reviewed diff. One Asker is one
// conversation, so each answer can build on the earlier ones. Implemented by
// app/ask; a nil Asker turns the side panel off.
type Asker interface {
	Ask(ctx context.Context, prompt string, onChunk func(string)) error
	SessionID() string
}

const (
	askContextLines = 3  // unchanged lines shown around a hunk asked about
	askMinWidth     = 28 // narrower than this the panel is not shown
	askMinDiffWidth = 40 // the diff pane keeps at least this many columns
)

// askTurn is one question and its answer in the side panel.
type askTurn struct {
	where    string // what was asked about, e.g. "app/main.go:42-44"
	question string
	answer   string
	err      string
	done     bool
}

// askState holds the Claude side panel: the conversation transcript, the
// question input, and the stream of the answer in flight.
type askState struct {
	asker     Asker
	visible   bool
	inputting bool
	input     textinput.Model
	pending   askTarget // what the question being typed is about
	turns     []askTurn
	running   bool
	events    chan tea.Msg // answer chunks and completion for the turn in flight
	offset    int          // first transcript row shown
	follow    bool         // keep the newest text in view while it streams
	asked     bool         // a question was sent, so the session exists
	hint      string       // transient status-bar message; cleared on next key press
}

// askTarget is the code a question is about.
type askTarget struct {
	where string // display label, e.g. "app/main.go:42-44"
	file  string // repo-relative path on its branch
	note  string // extra location detail, e.g. the stack branch
	lines []diff.DiffLine
}

type askChunkMsg struct{ text string }

type askDoneMsg struct{ err error }

// handleAskKey opens the question input for the selection, else the hunk
// under the cursor, else the cursor line.
func (m Model) handleAskKey() (tea.Model, tea.Cmd) {
	switch {
	case m.ask.asker == nil:
		m.ask.hint = "Claude panel is off (start revdiff with --ask)"
		return m, nil
	case m.ask.running:
		m.ask.hint = "Claude is still answering"
		m.showAskPanel(true)
		return m, nil
	case m.file.name == "" || len(m.file.lines) == 0:
		m.ask.hint = "Open a file to ask about it"
		return m, nil
	}

	target := m.askTarget()
	ti := textinput.New()
	ti.Placeholder = "ask about " + target.where
	ti.Prompt = "> "
	ti.Cursor.SetMode(cursor.CursorStatic) // static, like the annotation input: no blink re-renders
	cmd := ti.Focus()
	ti.CharLimit = annotCharLimit
	inputStyle := m.resolver.Style(style.StyleKeyAnnotInputText)
	ti.PromptStyle, ti.TextStyle = inputStyle, inputStyle
	cursorStyle := m.resolver.Style(style.StyleKeyAnnotInputCursor)
	ti.Cursor.TextStyle, ti.Cursor.Style = cursorStyle, cursorStyle
	ti.PlaceholderStyle = m.resolver.Style(style.StyleKeyAnnotInputPlaceholder)

	m.ask.pending = target
	m.ask.inputting = true
	m.showAskPanel(true)
	ti.Width = max(10, m.askPanelWidth()-3) // the panel width is known once it is visible
	m.ask.input = ti
	return m, cmd
}

// handleAskInputKey drives the question input: enter sends, esc cancels.
func (m Model) handleAskInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		question := strings.TrimSpace(m.ask.input.Value())
		m.ask.inputting = false
		if question == "" {
			return m, nil
		}
		return m.sendQuestion(question)
	case tea.KeyEsc:
		m.ask.inputting = false
		return m, nil
	default:
		var cmd tea.Cmd
		m.ask.input, cmd = m.ask.input.Update(msg)
		return m, cmd
	}
}

// sendQuestion starts answering in the background and streams the answer in.
func (m Model) sendQuestion(question string) (tea.Model, tea.Cmd) {
	target := m.ask.pending
	m.ask.turns = append(m.ask.turns, askTurn{where: target.where, question: question})
	m.ask.running = true
	m.ask.asked = true
	m.ask.follow = true

	events := make(chan tea.Msg, 64)
	m.ask.events = events
	asker, prompt := m.ask.asker, buildAskPrompt(target, question)
	go func() {
		err := asker.Ask(context.Background(), prompt, func(s string) { events <- askChunkMsg{text: s} })
		events <- askDoneMsg{err: err}
		close(events)
	}()
	return m, waitAsk(events)
}

// waitAsk delivers the next message of the answer in flight.
func waitAsk(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-events
		if !ok {
			return nil
		}
		return msg
	}
}

func (m Model) handleAskChunk(msg askChunkMsg) (tea.Model, tea.Cmd) {
	if n := len(m.ask.turns); n > 0 {
		m.ask.turns[n-1].answer += msg.text
	}
	return m, waitAsk(m.ask.events)
}

func (m Model) handleAskDone(msg askDoneMsg) (tea.Model, tea.Cmd) {
	m.ask.running = false
	if n := len(m.ask.turns); n > 0 {
		m.ask.turns[n-1].done = true
		if msg.err != nil {
			m.ask.turns[n-1].err = msg.err.Error()
		}
	}
	if !m.ask.visible {
		m.ask.hint = "Claude answered: press c to see it"
	}
	return m, nil
}

// handleAskPaneAction scrolls the transcript while the panel has focus.
// Actions it does not own fall through to the normal dispatch.
func (m Model) handleAskPaneAction(action keymap.Action) (tea.Model, tea.Cmd, bool) {
	page := max(1, m.askBodyHeight())
	switch action {
	case keymap.ActionDown:
		m.scrollAsk(1)
	case keymap.ActionUp:
		m.scrollAsk(-1)
	case keymap.ActionPageDown:
		m.scrollAsk(page)
	case keymap.ActionPageUp:
		m.scrollAsk(-page)
	case keymap.ActionHalfPageDown:
		m.scrollAsk(page / 2)
	case keymap.ActionHalfPageUp:
		m.scrollAsk(-page / 2)
	case keymap.ActionHome:
		m.ask.follow = false
		m.ask.offset = 0
	case keymap.ActionEnd:
		m.ask.follow = true
	case keymap.ActionDismiss:
		m.layout.focus = paneDiff
	default:
		return m, nil, false
	}
	return m, nil, true
}

// scrollAsk moves the transcript by delta rows; reaching the end resumes following.
func (m *Model) scrollAsk(delta int) {
	total, visible := len(m.askTranscriptRows()), m.askBodyHeight()
	maxOffset := max(0, total-visible)
	m.ask.offset = min(max(0, m.askOffset()+delta), maxOffset)
	m.ask.follow = m.ask.offset >= maxOffset
}

// showAskPanel shows or hides the panel and refits the diff pane around it.
func (m *Model) showAskPanel(visible bool) {
	if m.ask.visible == visible {
		return
	}
	m.ask.visible = visible
	if !visible && m.layout.focus == paneAsk {
		m.layout.focus = paneDiff
	}
	m.layout.viewport.Width = m.diffPaneWidth()
	m.syncViewportToCursor()
	m.layout.viewport.SetContent(m.renderDiff())
}

// askTarget picks the code the next question is about.
func (m Model) askTarget() askTarget {
	lines := m.selectedLines()
	if len(lines) == 0 {
		lines = m.askLinesAroundCursor()
	}
	file, note := m.file.name, ""
	if lvl := m.currentLevel(); lvl >= 0 {
		label := m.stack.labels[lvl]
		file = strings.TrimPrefix(m.file.name, label+"/")
		if _, branch, ok := strings.Cut(label, "~"); ok {
			note = fmt.Sprintf("branch %s, PR %d of %d in the stack", branch, lvl+1, len(m.stack.labels))
		}
	}
	return askTarget{where: file + ":" + lineSpan(lines), file: file, note: note, lines: lines}
}

// askLinesAroundCursor returns the change run under the cursor, or the cursor
// line, with a few unchanged lines either side for orientation.
func (m Model) askLinesAroundCursor() []diff.DiffLine {
	lines := m.file.lines
	cur := min(max(m.nav.diffCursor, 0), len(lines)-1)
	isChange := func(i int) bool {
		ct := lines[i].ChangeType
		return ct == diff.ChangeAdd || ct == diff.ChangeRemove
	}
	start, end := cur, cur
	if isChange(cur) {
		for start > 0 && isChange(start-1) {
			start--
		}
		for end < len(lines)-1 && isChange(end+1) {
			end++
		}
	}
	for i := 0; i < askContextLines && start > 0 && lines[start-1].ChangeType != diff.ChangeDivider; i++ {
		start--
	}
	for i := 0; i < askContextLines && end < len(lines)-1 && lines[end+1].ChangeType != diff.ChangeDivider; i++ {
		end++
	}
	out := make([]diff.DiffLine, 0, end-start+1)
	for _, dl := range lines[start : end+1] {
		if dl.ChangeType != diff.ChangeDivider {
			out = append(out, dl)
		}
	}
	return out
}

// lineSpan labels lines by their new-file numbers, or old numbers when every
// line was removed.
func lineSpan(lines []diff.DiffLine) string {
	lo, hi := 0, 0
	for _, dl := range lines {
		if dl.ChangeType == diff.ChangeRemove {
			continue
		}
		if lo == 0 {
			lo = dl.NewNum
		}
		hi = dl.NewNum
	}
	prefix := ""
	if lo == 0 && len(lines) > 0 {
		prefix, lo, hi = "old ", lines[0].OldNum, lines[len(lines)-1].OldNum
	}
	if lo == hi {
		return prefix + strconv.Itoa(lo)
	}
	return prefix + strconv.Itoa(lo) + "-" + strconv.Itoa(hi)
}

// buildAskPrompt frames a question with the exact lines it is about, numbered
// on both sides so the answer can refer to them.
func buildAskPrompt(t askTarget, question string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Question about %s", t.where)
	if t.note != "" {
		fmt.Fprintf(&b, " (%s)", t.note)
	}
	b.WriteString(".\n\nThe lines, as old/new line numbers, a +/- marker, and the code:\n```\n")
	for _, dl := range t.lines {
		marker := " "
		switch dl.ChangeType {
		case diff.ChangeAdd:
			marker = "+"
		case diff.ChangeRemove:
			marker = "-"
		}
		fmt.Fprintf(&b, "%5s %5s %s %s\n", lineNumOrBlank(dl.OldNum), lineNumOrBlank(dl.NewNum), marker, dl.Content)
	}
	b.WriteString("```\n\n")
	b.WriteString(question)
	return b.String()
}

func lineNumOrBlank(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// askPanelWidth returns the panel's inner width, 0 when it is hidden or the
// terminal is too narrow to fit it beside a usable diff pane.
func (m Model) askPanelWidth() int {
	if !m.ask.visible {
		return 0
	}
	used := 2 // diff pane borders
	if !m.treePaneHidden() {
		used += m.layout.treeWidth + 2
	}
	w := min(max(m.layout.width*35/100, askMinWidth), m.layout.width-used-askMinDiffWidth-2)
	if w < askMinWidth {
		return 0
	}
	return w
}

// diffPaneWidth returns the diff pane's inner width after the tree and the
// side panel, with their borders, take their columns.
func (m Model) diffPaneWidth() int {
	w := m.layout.width - 2
	if !m.treePaneHidden() {
		w -= m.layout.treeWidth + 2
	}
	if aw := m.askPanelWidth(); aw > 0 {
		w -= aw + 2
	}
	return w
}

// askBodyHeight is the number of transcript rows the panel shows.
func (m Model) askBodyHeight() int {
	h := m.paneHeight() - 1 // header row
	if m.ask.inputting {
		h -= 2 // separator and input rows
	}
	return max(1, h)
}

// askOffset returns the effective first transcript row, following the end
// while an answer streams in.
func (m Model) askOffset() int {
	maxOffset := max(0, len(m.askTranscriptRows())-m.askBodyHeight())
	if m.ask.follow {
		return maxOffset
	}
	return min(m.ask.offset, maxOffset)
}

// askTranscriptRows renders the conversation wrapped to the panel width.
func (m Model) askTranscriptRows() []string {
	w := m.askPanelWidth() - 1 // one column of left padding
	if w <= 0 {
		return nil
	}
	accent := string(m.resolver.Color(style.ColorKeyAccentFg))
	muted := string(m.resolver.Color(style.ColorKeyMutedFg))
	reset := string(style.ResetFg)
	if m.cfg.noColors {
		accent, muted, reset = "", "", ""
	}
	wrap := func(text string) []string {
		return strings.Split(ansi.Wrap(text, w, ""), "\n")
	}

	var rows []string
	if len(m.ask.turns) == 0 {
		rows = append(rows, wrapRows(muted, reset, wrap("Ask about the selection (V) or the hunk under the cursor. Press c to ask, tab to focus this panel, esc to close it."))...)
	}
	for i, t := range m.ask.turns {
		if i > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, wrapRows(accent, reset, wrap("you · "+t.where))...)
		rows = append(rows, wrap(t.question)...)
		rows = append(rows, "")
		switch {
		case t.answer != "":
			rows = append(rows, wrap(strings.TrimRight(t.answer, "\n"))...)
		case !t.done:
			rows = append(rows, wrapRows(muted, reset, wrap("thinking…"))...)
		}
		if t.err != "" {
			rows = append(rows, wrapRows(muted, reset, wrap("error: "+t.err))...)
		}
	}
	return rows
}

func wrapRows(color, reset string, rows []string) []string {
	if color == "" {
		return rows
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = color + r + reset
	}
	return out
}

// renderAskPanel renders the bordered side panel at the given pane height.
func (m Model) renderAskPanel(ph int) string {
	w := m.askPanelWidth()
	title := " Claude"
	if m.ask.running {
		title += " · answering…"
	}
	rows := []string{m.resolver.Style(style.StyleKeyDirEntry).Render(ansi.Truncate(title, w, "…"))}

	transcript := m.askTranscriptRows()
	offset, body := m.askOffset(), m.askBodyHeight()
	for i := range body {
		row := ""
		if offset+i < len(transcript) {
			row = " " + transcript[offset+i]
		}
		rows = append(rows, row)
	}
	if m.ask.inputting {
		rows = append(rows, strings.Repeat("─", w), m.ask.input.View())
	}

	content := m.padContentBg(strings.Join(rows, "\n"), w, m.resolver.Color(style.ColorKeyDiffPaneBg))
	paneStyle := m.resolver.Style(style.StyleKeyDiffPane)
	if m.layout.focus == paneAsk {
		paneStyle = m.resolver.Style(style.StyleKeyDiffPaneActive)
	}
	return paneStyle.Width(w).Height(ph).Render(content)
}

// askStatus renders the status-bar segment while an answer is in flight.
func (m Model) askStatus() string {
	if m.ask.running {
		return "◆ claude"
	}
	return ""
}
