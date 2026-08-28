package ui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// stackState holds stack-review navigation state. Empty labels means the
// session is not a stack review and the stack actions are inert.
type stackState struct {
	labels []string // synthetic level labels in stack order, e.g. ["1~feat-a", "2~feat-b"]
	hint   string   // transient status-bar message; cleared on next key press
}

// active reports whether this session is a stack review.
func (s stackState) active() bool { return len(s.labels) > 0 }

// levelOf returns the 0-based stack level owning path, or -1 when the path
// carries no known label. Matching is by whole label, longest first: branch
// names may contain "/", so a positional split on the first separator would
// attribute "2~feature/auth/app/main.go" to a level named "2~feature".
func (s stackState) levelOf(path string) int {
	best, bestLen := -1, -1
	for i, label := range s.labels {
		if strings.HasPrefix(path, label+"/") && len(label) > bestLen {
			best, bestLen = i, len(label)
		}
	}
	return best
}

// currentLevel returns the 0-based level of the file on screen, or -1.
func (m Model) currentLevel() int {
	if !m.stack.active() {
		return -1
	}
	return m.stack.levelOf(m.file.name)
}

// handleStackNav moves the selection to the first visible file of the adjacent
// stack level. It walks the tree's visible order rather than the label list, so
// levels hidden by the annotated-only or unreviewed filters are skipped and the
// jump always lands on a file that is actually on screen.
//
// Movement does not wrap. Wrapping from the top of a stack back to its base
// reads as a navigation bug rather than a convenience, so a jump past either
// end is a no-op with a hint.
func (m Model) handleStackNav(forward bool) (tea.Model, tea.Cmd) {
	if !m.stack.active() {
		return m, nil
	}

	files := m.tree.VisibleFiles()
	if len(files) == 0 {
		return m, nil
	}

	current := m.stack.levelOf(m.tree.SelectedFile())
	target, ok := m.nextStackFile(files, current, forward)
	if !ok {
		m.stack.hint = "Top of stack"
		if !forward {
			m.stack.hint = "Base of stack"
		}
		return m, nil
	}

	m.stack.hint = ""
	if !m.tree.SelectByPath(target) {
		return m, nil
	}
	return m.loadSelectedIfChanged()
}

// nextStackFile finds the first visible file belonging to a level beyond
// current in the requested direction. When current is -1 (no label on the
// selected path) the first file of the first labelled level is used going
// forward, and the last one going back.
func (m Model) nextStackFile(files []string, current int, forward bool) (path string, ok bool) {
	if forward {
		for _, f := range files {
			if lvl := m.stack.levelOf(f); lvl > current {
				return f, true
			}
		}
		return "", false
	}

	// backward: the target is the first file of the nearest lower level, so
	// scan forward and keep the best candidate rather than taking the last
	// file of that level
	best, bestLevel := "", -1
	for _, f := range files {
		lvl := m.stack.levelOf(f)
		if lvl < 0 || (current >= 0 && lvl >= current) {
			continue
		}
		if lvl > bestLevel {
			best, bestLevel = f, lvl
		}
	}
	return best, bestLevel >= 0
}

// stackPosition renders the "level/total" indicator for the status bar,
// empty when this is not a stack review or the current file carries no label.
func (m Model) stackPosition() string {
	lvl := m.currentLevel()
	if lvl < 0 {
		return ""
	}
	return strconv.Itoa(lvl+1) + "/" + strconv.Itoa(len(m.stack.labels))
}
