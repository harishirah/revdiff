package ui

import (
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/umputun/revdiff/app/diff"
	"github.com/umputun/revdiff/app/ui/style"
)

// selectionState holds a line-range selection in the diff pane. The range runs
// from anchor to the cursor, so moving the cursor extends it. A selection
// belongs to the file it was started in and is cleared when another file loads.
type selectionState struct {
	active bool
	anchor int    // diff-line index where the selection started
	file   string // file the selection belongs to
	hint   string // transient status-bar message; cleared on next key press
}

// selectionRange returns the selected diff-line indices, inclusive and ordered.
func (m Model) selectionRange() (start, end int, ok bool) {
	if !m.sel.active || m.sel.file != m.file.name || len(m.file.lines) == 0 {
		return 0, 0, false
	}
	start, end = m.sel.anchor, m.nav.diffCursor
	if start > end {
		start, end = end, start
	}
	start = max(start, 0)
	end = min(end, len(m.file.lines)-1)
	return start, end, start <= end
}

// isSelectedLine reports whether the diff line at idx is inside the selection.
func (m Model) isSelectedLine(idx int) bool {
	start, end, ok := m.selectionRange()
	return ok && idx >= start && idx <= end
}

// selectedLines returns the selected diff lines, dividers excluded.
func (m Model) selectedLines() []diff.DiffLine {
	start, end, ok := m.selectionRange()
	if !ok {
		return nil
	}
	out := make([]diff.DiffLine, 0, end-start+1)
	for _, dl := range m.file.lines[start : end+1] {
		if dl.ChangeType != diff.ChangeDivider {
			out = append(out, dl)
		}
	}
	return out
}

// selectionAnnotationTarget maps the selection to the single number space an
// annotation range needs. A selection of only removed lines uses old line
// numbers; anything else uses new line numbers from its first to its last
// non-removed line, so removed lines in between are covered implicitly. idx is
// the diff-line index the annotation attaches to; endLine is 0 for one line.
func (m Model) selectionAnnotationTarget() (idx, line, endLine int, changeType diff.ChangeType, ok bool) {
	start, end, ok := m.selectionRange()
	if !ok {
		return 0, 0, 0, "", false
	}
	first, last := -1, -1
	for i := start; i <= end; i++ {
		ct := m.file.lines[i].ChangeType
		if ct == diff.ChangeDivider || ct == diff.ChangeRemove {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first < 0 { // only removed lines (and dividers)
		for i := start; i <= end; i++ {
			if m.file.lines[i].ChangeType != diff.ChangeRemove {
				continue
			}
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return 0, 0, 0, "", false
	}
	line, endLine = m.diffLineNum(m.file.lines[first]), m.diffLineNum(m.file.lines[last])
	if endLine <= line {
		endLine = 0
	}
	return first, line, endLine, m.file.lines[first].ChangeType, true
}

// handleSelectToggle starts a selection at the cursor, or clears an active one.
func (m Model) handleSelectToggle() (tea.Model, tea.Cmd) {
	if m.sel.active {
		m.clearSelection()
		return m, nil
	}
	if m.layout.focus != paneDiff || m.annot.cursorOnAnnotation || m.nav.diffCursor < 0 || len(m.file.lines) == 0 {
		m.sel.hint = "Select lines from the diff pane"
		return m, nil
	}
	m.sel = selectionState{active: true, anchor: m.nav.diffCursor, file: m.file.name}
	m.sel.hint = "Selecting: move to extend, a to annotate, V or esc to cancel"
	m.layout.viewport.SetContent(m.renderDiff())
	return m, nil
}

// clearSelection drops the selection and repaints the diff.
func (m *Model) clearSelection() {
	if !m.sel.active {
		return
	}
	m.sel = selectionState{}
	m.layout.viewport.SetContent(m.renderDiff())
}

// selectionStatus renders the status-bar segment for an active selection.
func (m Model) selectionStatus() string {
	start, end, ok := m.selectionRange()
	if !ok {
		return ""
	}
	return "▌" + strconv.Itoa(end-start+1) + " lines"
}

// gutterCell renders the one-column gutter left of a diff row: the cursor
// marker, the selection marker, or a blank.
func (m Model) gutterCell(idx int, isCursor bool) string {
	switch {
	case isCursor:
		return m.renderer.DiffCursor(m.cfg.noColors)
	case !m.isSelectedLine(idx):
		return " "
	case m.cfg.noColors:
		return "▌"
	default:
		return string(m.resolver.Color(style.ColorKeyAccentFg)) + string(m.resolver.Color(style.ColorKeyDiffPaneBg)) +
			"▌" + string(style.ResetFg) + string(style.ResetBg)
	}
}
