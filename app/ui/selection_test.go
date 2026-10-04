package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revdiff/app/diff"
)

// selectionTestModel loads a.go with a replace hunk: two removed lines
// (old 2-3) followed by two added lines (new 2-3), between context lines.
func selectionTestModel() Model {
	m := testModel([]string{"a.go"}, nil)
	m.tree = testNewFileTree([]string{"a.go"})
	m.layout.focus = paneDiff
	m.file.name = "a.go"
	m.file.lines = []diff.DiffLine{
		{ChangeType: diff.ChangeContext, OldNum: 1, NewNum: 1, Content: "a"}, // 0
		{ChangeType: diff.ChangeRemove, OldNum: 2, Content: "b"},             // 1
		{ChangeType: diff.ChangeRemove, OldNum: 3, Content: "c"},             // 2
		{ChangeType: diff.ChangeAdd, NewNum: 2, Content: "B"},                // 3
		{ChangeType: diff.ChangeAdd, NewNum: 3, Content: "C"},                // 4
		{ChangeType: diff.ChangeContext, OldNum: 4, NewNum: 4, Content: "d"}, // 5
	}
	m.nav.diffCursor = 1
	return m
}

func pressRune(t *testing.T, m Model, r rune) Model {
	t.Helper()
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return result.(Model)
}

func TestSelection_toggleExtendsWithCursor(t *testing.T) {
	m := pressRune(t, selectionTestModel(), 'V')
	require.True(t, m.sel.active)
	assert.NotEmpty(t, m.sel.hint)

	m.nav.diffCursor = 4
	start, end, ok := m.selectionRange()
	require.True(t, ok)
	assert.Equal(t, 1, start)
	assert.Equal(t, 4, end)
	assert.False(t, m.isSelectedLine(0))
	assert.True(t, m.isSelectedLine(2))
	assert.Equal(t, "▌4 lines", m.selectionStatus())

	m.nav.diffCursor = 0 // moving above the anchor selects upward
	start, end, _ = m.selectionRange()
	assert.Equal(t, 0, start)
	assert.Equal(t, 1, end)

	m = pressRune(t, m, 'V')
	assert.False(t, m.sel.active)
	assert.Empty(t, m.selectionStatus())
}

func TestSelection_needsDiffPane(t *testing.T) {
	m := selectionTestModel()
	m.layout.focus = paneTree
	m = pressRune(t, m, 'V')
	assert.False(t, m.sel.active)
	assert.Equal(t, "Select lines from the diff pane", m.sel.hint)
}

func TestSelection_escClears(t *testing.T) {
	m := pressRune(t, selectionTestModel(), 'V')
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, result.(Model).sel.active)
}

func TestSelection_clearedWhenAnotherFileLoads(t *testing.T) {
	m := pressRune(t, selectionTestModel(), 'V')
	result, _ := m.handleFileLoaded(fileLoadedMsg{file: "b.go", seq: m.file.loadSeq,
		lines: []diff.DiffLine{{ChangeType: diff.ChangeAdd, NewNum: 1, Content: "x"}}})
	assert.False(t, result.(Model).sel.active)
}

func TestSelection_annotationTarget(t *testing.T) {
	tests := []struct {
		name         string
		anchor, cur  int
		wantIdx      int
		wantLine     int
		wantEnd      int
		wantType     diff.ChangeType
		wantSelected bool
	}{
		{name: "only removed lines use old numbers", anchor: 1, cur: 2, wantIdx: 1, wantLine: 2, wantEnd: 3, wantType: diff.ChangeRemove},
		{name: "removed then added uses the added range", anchor: 1, cur: 4, wantIdx: 3, wantLine: 2, wantEnd: 3, wantType: diff.ChangeAdd},
		{name: "context start keeps new numbers", anchor: 0, cur: 3, wantIdx: 0, wantLine: 1, wantEnd: 2, wantType: diff.ChangeContext},
		{name: "one line has no end", anchor: 3, cur: 3, wantIdx: 3, wantLine: 2, wantEnd: 0, wantType: diff.ChangeAdd},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := selectionTestModel()
			m.sel = selectionState{active: true, anchor: tt.anchor, file: "a.go"}
			m.nav.diffCursor = tt.cur
			idx, line, end, ct, ok := m.selectionAnnotationTarget()
			require.True(t, ok)
			assert.Equal(t, tt.wantIdx, idx)
			assert.Equal(t, tt.wantLine, line)
			assert.Equal(t, tt.wantEnd, end)
			assert.Equal(t, tt.wantType, ct)
		})
	}
}

func TestSelection_annotateSavesRange(t *testing.T) {
	m := selectionTestModel()
	m.sel = selectionState{active: true, anchor: 1, file: "a.go"}
	m.nav.diffCursor = 4

	m.startAnnotation()
	assert.False(t, m.sel.active, "annotating consumes the selection")
	assert.Equal(t, 3, m.nav.diffCursor, "input opens under the first added line")
	m.annot.input.SetValue("extract these into a helper")
	m.saveAnnotation()

	anns := m.store.Get("a.go")
	require.Len(t, anns, 1)
	assert.Equal(t, 2, anns[0].Line)
	assert.Equal(t, 3, anns[0].EndLine)
	assert.Equal(t, "+", anns[0].Type)
	assert.Contains(t, m.store.FormatOutput(), "## a.go:2-3 (+)\nextract these into a helper\n")
	assert.Zero(t, m.annot.rangeEnd)
}

func TestSelection_editorSaveKeepsRange(t *testing.T) {
	m := selectionTestModel()
	result, _ := m.handleEditorFinished(editorFinishedMsg{content: "multi\nline", fileName: "a.go", line: 2, endLine: 3, changeType: "+"})
	anns := result.(Model).store.Get("a.go")
	require.Len(t, anns, 1)
	assert.Equal(t, 3, anns[0].EndLine)
}

func TestSelection_gutterMarksSelectedRows(t *testing.T) {
	m := selectionTestModel()
	m.sel = selectionState{active: true, anchor: 1, file: "a.go"}
	m.nav.diffCursor = 3

	assert.Equal(t, " ", m.gutterCell(0, false))
	assert.Contains(t, m.gutterCell(2, false), "▌")
	assert.Equal(t, m.renderer.DiffCursor(false), m.gutterCell(3, true), "the cursor marker wins on the cursor row")

	out := m.renderDiff()
	assert.Equal(t, 2, strings.Count(out, "▌"), "rows 1 and 2 are marked; row 3 shows the cursor")

	// collapsed view hides removed rows, so select across the hunk instead
	m.modes.collapsed.enabled = true
	m.sel.anchor, m.nav.diffCursor = 0, 5
	assert.Equal(t, 3, strings.Count(m.renderDiff(), "▌"), "context row 0 and added rows 3-4; row 5 shows the cursor")
}
