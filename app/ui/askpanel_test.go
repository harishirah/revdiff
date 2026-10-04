package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revdiff/app/diff"
)

// fakeAsker streams fixed chunks and records the prompts it was given.
type fakeAsker struct {
	mu      sync.Mutex
	prompts []string
	chunks  []string
	err     error
}

func (f *fakeAsker) Ask(_ context.Context, prompt string, onChunk func(string)) error {
	f.mu.Lock()
	f.prompts = append(f.prompts, prompt)
	f.mu.Unlock()
	for _, c := range f.chunks {
		onChunk(c)
	}
	return f.err
}

func (f *fakeAsker) SessionID() string { return "sess-1" }

func (f *fakeAsker) lastPrompt() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prompts[len(f.prompts)-1]
}

func askTestModel(asker *fakeAsker) Model {
	m := selectionTestModel()
	m.ask.asker = asker
	return m
}

// typeQuestion opens the input with c, types q, and sends it, returning the
// model with the answer fully streamed in.
func typeQuestion(t *testing.T, m Model, q string) Model {
	t.Helper()
	m = pressRune(t, m, 'c')
	require.True(t, m.ask.inputting, "c opens the question input")
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(q)})
	m = result.(Model)
	result, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = result.(Model)
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			break
		}
		result, cmd = m.Update(msg)
		m = result.(Model)
	}
	return m
}

func TestAsk_offWithoutAsker(t *testing.T) {
	m := pressRune(t, selectionTestModel(), 'c')
	assert.False(t, m.ask.visible)
	assert.Contains(t, m.ask.hint, "--ask")
}

func TestAsk_asksAboutTheSelectionAndStreamsTheAnswer(t *testing.T) {
	asker := &fakeAsker{chunks: []string{"It replaces b and c ", "with B and C."}}
	m := askTestModel(asker)
	m.sel = selectionState{active: true, anchor: 3, file: "a.go"}
	m.nav.diffCursor = 4

	m = typeQuestion(t, m, "why the rename?")

	require.Len(t, m.ask.turns, 1)
	turn := m.ask.turns[0]
	assert.Equal(t, "a.go:2-3", turn.where)
	assert.Equal(t, "why the rename?", turn.question)
	assert.Equal(t, "It replaces b and c with B and C.", turn.answer)
	assert.True(t, turn.done)
	assert.False(t, m.ask.running)
	assert.True(t, m.sel.active, "the selection survives so it can be annotated next")

	prompt := asker.lastPrompt()
	assert.True(t, strings.HasPrefix(prompt, "Question about a.go:2-3."))
	assert.Contains(t, prompt, "\n          2 + B\n          3 + C\n")
	assert.True(t, strings.HasSuffix(prompt, "why the rename?"))

	rows := strings.Join(m.askTranscriptRows(), "\n")
	assert.Contains(t, rows, "you · a.go:2-3")
	assert.Contains(t, rows, "with B and C.")
}

func TestAsk_withoutSelectionAsksAboutTheHunk(t *testing.T) {
	asker := &fakeAsker{chunks: []string{"ok"}}
	m := askTestModel(asker)
	m.nav.diffCursor = 1 // inside the replace hunk

	m = typeQuestion(t, m, "what changed?")
	assert.Equal(t, "a.go:1-4", m.ask.turns[0].where, "the whole hunk plus surrounding context")
	assert.Contains(t, asker.lastPrompt(), "    2       - b\n")
}

func TestAsk_stackFileNamesItsBranch(t *testing.T) {
	asker := &fakeAsker{chunks: []string{"ok"}}
	m := askTestModel(asker)
	m.stack = stackState{labels: []string{"1~feat-a", "2~feature/ui"}}
	m.file.name = "2~feature/ui/app/view.go"
	m.sel = selectionState{active: true, anchor: 3, file: m.file.name}
	m.nav.diffCursor = 3

	m = typeQuestion(t, m, "why?")
	assert.Equal(t, "app/view.go:2", m.ask.turns[0].where)
	assert.Contains(t, asker.lastPrompt(), "(branch feature/ui, PR 2 of 2 in the stack)")
}

func TestAsk_errorIsShownInThePanel(t *testing.T) {
	m := typeQuestion(t, askTestModel(&fakeAsker{err: errors.New("not logged in")}), "q")
	assert.Equal(t, "not logged in", m.ask.turns[0].err)
	assert.Contains(t, strings.Join(m.askTranscriptRows(), "\n"), "error: not logged in")
}

func TestAsk_secondQuestionWhileAnswering(t *testing.T) {
	m := askTestModel(&fakeAsker{})
	m.ask.running = true
	m = pressRune(t, m, 'c')
	assert.False(t, m.ask.inputting)
	assert.Equal(t, "Claude is still answering", m.ask.hint)
}

func TestAsk_panelTakesItsColumnsFromTheDiff(t *testing.T) {
	m := askTestModel(&fakeAsker{})
	before := m.diffPaneWidth()
	m.showAskPanel(true)

	aw := m.askPanelWidth()
	require.Positive(t, aw)
	assert.Equal(t, before-aw-2, m.diffPaneWidth())
	assert.Equal(t, m.diffPaneWidth(), m.layout.viewport.Width)
	assert.Contains(t, m.View(), "Claude")

	m.layout.width = 80 // tree + a 40-column diff leaves no room for the panel
	assert.Zero(t, m.askPanelWidth())
}

func TestAsk_focusCyclesAndEscCloses(t *testing.T) {
	m := typeQuestion(t, askTestModel(&fakeAsker{chunks: []string{"answer"}}), "q")
	require.True(t, m.ask.visible)

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = result.(Model)
	assert.Equal(t, paneAsk, m.layout.focus)
	m = pressRune(t, m, 'k') // scrolling the transcript must not move the diff cursor
	assert.Equal(t, 1, m.nav.diffCursor)

	result, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = result.(Model)
	assert.Equal(t, paneDiff, m.layout.focus, "esc in the panel returns to the diff")
	assert.True(t, m.ask.visible)

	m.sel = selectionState{} // esc clears a selection before it closes the panel
	result, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, result.(Model).ask.visible)
}

func TestAsk_sessionShownInInfo(t *testing.T) {
	m := typeQuestion(t, askTestModel(&fakeAsker{chunks: []string{"a"}}), "q")
	m.review.cfg = &ReviewInfoConfig{}
	var found bool
	for _, row := range m.reviewRows() {
		if row.Label == "claude" {
			found = true
			assert.Equal(t, "claude --resume sess-1", row.Value)
		}
	}
	assert.True(t, found)
}

func TestLineSpan(t *testing.T) {
	add := func(n int) diff.DiffLine { return diff.DiffLine{ChangeType: diff.ChangeAdd, NewNum: n} }
	rm := func(n int) diff.DiffLine { return diff.DiffLine{ChangeType: diff.ChangeRemove, OldNum: n} }
	assert.Equal(t, "7", lineSpan([]diff.DiffLine{add(7)}))
	assert.Equal(t, "7-9", lineSpan([]diff.DiffLine{rm(3), add(7), add(9)}))
	assert.Equal(t, "old 3-4", lineSpan([]diff.DiffLine{rm(3), rm(4)}))
}
