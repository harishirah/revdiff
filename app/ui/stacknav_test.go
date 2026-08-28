package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revdiff/app/annotation"
	"github.com/umputun/revdiff/app/diff"
)

// stackTestModel builds a loaded model over a three-level stack whose labels
// exercise the awkward shapes: a plain branch, one containing a slash, and one
// whose name is a prefix of another level's.
func stackTestModel(t *testing.T) Model {
	t.Helper()
	labels := []string{"1~feat-a", "2~feature/ui", "3~feat"}
	files := []string{
		"1~feat-a/app/main.go", "1~feat-a/README.md",
		"2~feature/ui/app/view.go",
		"3~feat/docs/x.md",
	}
	m := testModel(files, nil)
	m.stack = stackState{labels: labels}
	entries := make([]diff.FileEntry, len(files))
	for i, f := range files {
		entries[i] = diff.FileEntry{Path: f}
	}
	m.tree.Rebuild(entries)
	require.True(t, m.tree.SelectByPath(files[0]))
	m.file.name = files[0]
	return m
}

func TestStackState_levelOf(t *testing.T) {
	s := stackState{labels: []string{"1~feat-a", "2~feature/ui", "3~feat"}}
	tests := []struct {
		name string
		path string
		want int
	}{
		{"first level", "1~feat-a/app/main.go", 0},
		{"slash in branch name", "2~feature/ui/app/view.go", 1},
		{"third level", "3~feat/docs/x.md", 2},
		{"unlabelled path", "app/main.go", -1},
		{"unknown label", "9~other/app/main.go", -1},
		{"label without trailing slash", "1~feat-a", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, s.levelOf(tt.path))
		})
	}
}

// a label that is a string prefix of another must not steal its files:
// "3~feat" prefixes nothing here, but "1~feat-a" and a hypothetical "1~feat"
// would collide under a naive HasPrefix without the longest-match rule.
func TestStackState_levelOf_longestMatchWins(t *testing.T) {
	s := stackState{labels: []string{"1~feat", "2~feat/sub"}}
	assert.Equal(t, 0, s.levelOf("1~feat/app/main.go"))
	assert.Equal(t, 1, s.levelOf("2~feat/sub/app/main.go"))
}

func TestStackState_inactive(t *testing.T) {
	var s stackState
	assert.False(t, s.active())
	assert.Equal(t, -1, s.levelOf("anything"))
}

func TestModel_handleStackNav_forward(t *testing.T) {
	m := stackTestModel(t)
	require.Equal(t, 0, m.currentLevel())

	next, _ := m.handleStackNav(true)
	m = next.(Model)
	assert.Equal(t, "2~feature/ui/app/view.go", m.tree.SelectedFile())
	assert.Empty(t, m.stack.hint)

	next, _ = m.handleStackNav(true)
	m = next.(Model)
	assert.Equal(t, "3~feat/docs/x.md", m.tree.SelectedFile())
}

func TestModel_handleStackNav_backward(t *testing.T) {
	m := stackTestModel(t)
	require.True(t, m.tree.SelectByPath("3~feat/docs/x.md"))
	m.file.name = "3~feat/docs/x.md"

	next, _ := m.handleStackNav(false)
	m = next.(Model)
	assert.Equal(t, "2~feature/ui/app/view.go", m.tree.SelectedFile())

	// backward lands on the FIRST file of the lower level, not its last
	next, _ = m.handleStackNav(false)
	m = next.(Model)
	assert.Equal(t, "1~feat-a/README.md", m.tree.SelectedFile(),
		"tree sorts within a level, so the first visible file of level 1 is README.md")
}

// movement must not wrap: wrapping from the top of a stack back to its base
// reads as a navigation bug rather than a convenience.
func TestModel_handleStackNav_noWrapAtBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		start    string
		forward  bool
		wantHint string
	}{
		{"top of stack", "3~feat/docs/x.md", true, "Top of stack"},
		{"base of stack", "1~feat-a/app/main.go", false, "Base of stack"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := stackTestModel(t)
			require.True(t, m.tree.SelectByPath(tt.start))
			m.file.name = tt.start

			next, cmd := m.handleStackNav(tt.forward)
			m = next.(Model)
			assert.Nil(t, cmd, "a boundary jump must not trigger a load")
			assert.Equal(t, tt.start, m.tree.SelectedFile(), "selection must not move")
			assert.Equal(t, tt.wantHint, m.stack.hint)
		})
	}
}

func TestModel_handleStackNav_inertWithoutStack(t *testing.T) {
	m := testModel([]string{"app/main.go", "app/other.go"}, nil)
	before := m.tree.SelectedFile()

	next, cmd := m.handleStackNav(true)
	m = next.(Model)
	assert.Nil(t, cmd)
	assert.Equal(t, before, m.tree.SelectedFile())
	assert.Empty(t, m.stack.hint)
}

func TestModel_handleStackNav_singleLevel(t *testing.T) {
	m := testModel([]string{"1~only/app/main.go"}, nil)
	m.stack = stackState{labels: []string{"1~only"}}
	m.tree.Rebuild([]diff.FileEntry{{Path: "1~only/app/main.go"}})
	require.True(t, m.tree.SelectByPath("1~only/app/main.go"))
	m.file.name = "1~only/app/main.go"

	next, _ := m.handleStackNav(true)
	assert.Equal(t, "Top of stack", next.(Model).stack.hint)

	next, _ = m.handleStackNav(false)
	assert.Equal(t, "Base of stack", next.(Model).stack.hint)
}

// navigation walks the tree's visible order, so a level hidden by the
// annotated-only filter is skipped rather than jumped into and found empty.
func TestModel_handleStackNav_respectsAnnotatedFilter(t *testing.T) {
	m := stackTestModel(t)
	m.store = annotation.NewStore()
	m.store.Add(annotation.Annotation{File: "1~feat-a/app/main.go", Line: 1, Type: "+", Comment: "x"})
	m.store.Add(annotation.Annotation{File: "3~feat/docs/x.md", Line: 1, Type: "+", Comment: "y"})
	m.tree.ToggleFilter(m.annotatedFiles())
	require.True(t, m.tree.SelectByPath("1~feat-a/app/main.go"))
	m.file.name = "1~feat-a/app/main.go"

	next, _ := m.handleStackNav(true)
	m = next.(Model)
	assert.Equal(t, "3~feat/docs/x.md", m.tree.SelectedFile(), "level 2 has no annotated file, so it is skipped")
}

func TestModel_stackPosition(t *testing.T) {
	m := stackTestModel(t)
	assert.Equal(t, "1/3", m.stackPosition())

	m.file.name = "2~feature/ui/app/view.go"
	assert.Equal(t, "2/3", m.stackPosition())

	m.file.name = "unlabelled.go"
	assert.Empty(t, m.stackPosition(), "an unlabelled file yields no indicator")

	assert.Empty(t, testModel([]string{"a.go"}, nil).stackPosition(), "non-stack sessions show nothing")
}

func TestModel_statusBar_showsStackPosition(t *testing.T) {
	m := stackTestModel(t)
	assert.Contains(t, m.statusBarText(), "⇅ 1/3")

	plain := testModel([]string{"a.go"}, nil)
	assert.NotContains(t, plain.statusBarText(), "⇅")
}
