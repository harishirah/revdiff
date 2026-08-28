package diff_test

import (
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revdiff/app/diff"
	"github.com/umputun/revdiff/app/diff/mocks"
)

// echoRenderer returns one entry per configured path and records every request.
func echoRenderer(paths ...string) *mocks.RendererMock {
	return &mocks.RendererMock{
		ChangedFilesFunc: func(string, bool) ([]diff.FileEntry, error) {
			entries := make([]diff.FileEntry, 0, len(paths))
			for _, p := range paths {
				entries = append(entries, diff.FileEntry{Path: p, Status: diff.FileModified})
			}
			return entries, nil
		},
		FileDiffFunc: func(req diff.FileDiffRequest) ([]diff.DiffLine, error) {
			return []diff.DiffLine{{Content: req.Path, ChangeType: diff.ChangeContext}}, nil
		},
	}
}

func levels(t *testing.T, specs ...[2]string) ([]diff.StackLevel, []*mocks.RendererMock) {
	t.Helper()
	out := make([]diff.StackLevel, 0, len(specs))
	inners := make([]*mocks.RendererMock, 0, len(specs))
	for i, s := range specs {
		inner := echoRenderer(fmt.Sprintf("app/f%d.go", i+1))
		inners = append(inners, inner)
		out = append(out, diff.StackLevel{Base: s[0], Head: s[1], Inner: inner})
	}
	return out, inners
}

func TestNewStackRenderer_labels(t *testing.T) {
	tests := []struct {
		name  string
		heads []string
		want  []string
	}{
		{"single", []string{"a"}, []string{"1~a"}},
		{"three levels, one digit", []string{"a", "b", "c"}, []string{"1~a", "2~b", "3~c"}},
		{"slash in branch name", []string{"feature/auth", "fix/ui"}, []string{"1~feature/auth", "2~fix/ui"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specs := make([][2]string, 0, len(tt.heads))
			base := "main"
			for _, h := range tt.heads {
				specs = append(specs, [2]string{base, h})
				base = h
			}
			lvls, _ := levels(t, specs...)
			s, err := diff.NewStackRenderer(lvls)
			require.NoError(t, err)
			assert.Equal(t, tt.want, s.Labels())
		})
	}
}

// ordinal width must grow with the level count so a lexical sort of prefixed
// paths - which is what the file tree does - reproduces stack order.
func TestNewStackRenderer_ordinalWidth(t *testing.T) {
	makeStack := func(n int) *diff.StackRenderer {
		specs := make([][2]string, 0, n)
		base := "main"
		for i := 1; i <= n; i++ {
			head := fmt.Sprintf("b%d", i)
			specs = append(specs, [2]string{base, head})
			base = head
		}
		lvls, _ := levels(t, specs...)
		s, err := diff.NewStackRenderer(lvls)
		require.NoError(t, err)
		return s
	}

	nine := makeStack(9)
	assert.Equal(t, "1~b1", nine.Labels()[0], "9 levels need one digit")
	assert.Equal(t, "9~b9", nine.Labels()[8])

	ten := makeStack(10)
	assert.Equal(t, "01~b1", ten.Labels()[0], "10 levels need two digits")
	assert.Equal(t, "10~b10", ten.Labels()[9])

	// the point of the padding: lexical order must equal stack order
	labels := append([]string(nil), ten.Labels()...)
	sorted := append([]string(nil), labels...)
	sort.Strings(sorted)
	assert.Equal(t, labels, sorted, "zero-padded labels must sort into stack order")
}

func TestNewStackRenderer_errors(t *testing.T) {
	inner := echoRenderer("a.go")
	tests := []struct {
		name   string
		levels []diff.StackLevel
		errMsg string
	}{
		{"no levels", nil, "no levels"},
		{"empty head", []diff.StackLevel{{Base: "main", Inner: inner}}, "has no head"},
		{"empty base", []diff.StackLevel{{Head: "a", Inner: inner}}, "has no base"},
		{"nil inner", []diff.StackLevel{{Base: "main", Head: "a"}}, "has no renderer"},
		{"duplicate head", []diff.StackLevel{
			{Base: "main", Head: "a", Inner: inner},
			{Base: "a", Head: "a", Inner: inner},
		}, `duplicate head "a"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := diff.NewStackRenderer(tt.levels)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestStackRenderer_ChangedFiles(t *testing.T) {
	inner1 := echoRenderer("app/main.go", "README.md")
	inner2 := echoRenderer("app/main.go", "app/ui/view.go")
	s, err := diff.NewStackRenderer([]diff.StackLevel{
		{Base: "main", Head: "feat-a", Inner: inner1},
		{Base: "feat-a", Head: "feat-b", Inner: inner2},
	})
	require.NoError(t, err)

	files, err := s.ChangedFiles("ignored", true)
	require.NoError(t, err)
	assert.Equal(t, []diff.FileEntry{
		{Path: "1~feat-a/app/main.go", Status: diff.FileModified},
		{Path: "1~feat-a/README.md", Status: diff.FileModified},
		{Path: "2~feat-b/app/main.go", Status: diff.FileModified},
		{Path: "2~feat-b/app/ui/view.go", Status: diff.FileModified},
	}, files, "the same file in two levels must yield two distinct paths")

	require.Len(t, inner1.ChangedFilesCalls(), 1)
	assert.Equal(t, "main..feat-a", inner1.ChangedFilesCalls()[0].Ref)
	assert.False(t, inner1.ChangedFilesCalls()[0].Staged, "staged is meaningless against a ref range")
	assert.Equal(t, "feat-a..feat-b", inner2.ChangedFilesCalls()[0].Ref)
}

func TestStackRenderer_ChangedFiles_rename(t *testing.T) {
	inner := &mocks.RendererMock{
		ChangedFilesFunc: func(string, bool) ([]diff.FileEntry, error) {
			return []diff.FileEntry{
				{Path: "app/new.go", OldPath: "app/old.go", Status: diff.FileRenamed},
				{Path: "app/plain.go", Status: diff.FileModified},
			}, nil
		},
	}
	s, err := diff.NewStackRenderer([]diff.StackLevel{{Base: "main", Head: "feat", Inner: inner}})
	require.NoError(t, err)

	files, err := s.ChangedFiles("", false)
	require.NoError(t, err)
	assert.Equal(t, []diff.FileEntry{
		{Path: "1~feat/app/new.go", OldPath: "1~feat/app/old.go", Status: diff.FileRenamed},
		{Path: "1~feat/app/plain.go", Status: diff.FileModified},
	}, files, "OldPath must be prefixed too, and only when set")
}

func TestStackRenderer_ChangedFiles_innerError(t *testing.T) {
	boom := errors.New("boom")
	inner := &mocks.RendererMock{
		ChangedFilesFunc: func(string, bool) ([]diff.FileEntry, error) { return nil, boom },
	}
	s, err := diff.NewStackRenderer([]diff.StackLevel{{Base: "main", Head: "feat", Inner: inner}})
	require.NoError(t, err)

	_, err = s.ChangedFiles("", false)
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "stack renderer, changed files for main..feat")
}

// the highest-value case: Model.fetchEffectiveFileDiff retries added files with
// Staged=true, and "git diff --cached a..b" is a usage error, so a leaked
// Staged would break every added file in the stack.
func TestStackRenderer_FileDiff_forcesStagedFalse(t *testing.T) {
	inner := echoRenderer("app/main.go")
	s, err := diff.NewStackRenderer([]diff.StackLevel{{Base: "main", Head: "feat", Inner: inner}})
	require.NoError(t, err)

	_, err = s.FileDiff(diff.FileDiffRequest{Path: "1~feat/app/main.go", Staged: true, ContextLines: 3})
	require.NoError(t, err)

	require.Len(t, inner.FileDiffCalls(), 1)
	got := inner.FileDiffCalls()[0].Req
	assert.False(t, got.Staged, "staged must be zeroed before delegating")
	assert.Equal(t, "app/main.go", got.Path, "label prefix must be stripped")
	assert.Equal(t, "main..feat", got.Ref, "ref comes from the level, not the caller")
	assert.Equal(t, 3, got.ContextLines, "context lines pass through")
}

func TestStackRenderer_FileDiff_routing(t *testing.T) {
	inner1, inner2 := echoRenderer("a.go"), echoRenderer("b.go")
	s, err := diff.NewStackRenderer([]diff.StackLevel{
		{Base: "main", Head: "feat-a", Inner: inner1},
		{Base: "feat-a", Head: "feat-b", Inner: inner2},
	})
	require.NoError(t, err)

	_, err = s.FileDiff(diff.FileDiffRequest{Path: "2~feat-b/app/main.go"})
	require.NoError(t, err)
	assert.Empty(t, inner1.FileDiffCalls(), "level 1 must not be consulted")
	require.Len(t, inner2.FileDiffCalls(), 1)
	assert.Equal(t, "app/main.go", inner2.FileDiffCalls()[0].Req.Path)
}

// branch names may contain "/", so stripping must match the whole label,
// never split on the first separator.
func TestStackRenderer_FileDiff_slashInBranchName(t *testing.T) {
	inner := echoRenderer("app/main.go")
	s, err := diff.NewStackRenderer([]diff.StackLevel{{Base: "main", Head: "feature/auth", Inner: inner}})
	require.NoError(t, err)

	_, err = s.FileDiff(diff.FileDiffRequest{Path: "1~feature/auth/app/main.go"})
	require.NoError(t, err)
	require.Len(t, inner.FileDiffCalls(), 1)
	assert.Equal(t, "app/main.go", inner.FileDiffCalls()[0].Req.Path)
}

func TestStackRenderer_FileDiff_renameStripsOldPath(t *testing.T) {
	inner := echoRenderer("app/new.go")
	s, err := diff.NewStackRenderer([]diff.StackLevel{{Base: "main", Head: "feat", Inner: inner}})
	require.NoError(t, err)

	_, err = s.FileDiff(diff.FileDiffRequest{Path: "1~feat/app/new.go", OldPath: "1~feat/app/old.go"})
	require.NoError(t, err)
	assert.Equal(t, "app/old.go", inner.FileDiffCalls()[0].Req.OldPath)
}

func TestStackRenderer_FileDiff_unknownPath(t *testing.T) {
	inner := echoRenderer("a.go")
	s, err := diff.NewStackRenderer([]diff.StackLevel{{Base: "main", Head: "feat", Inner: inner}})
	require.NoError(t, err)

	tests := []struct{ name, path, oldPath string }{
		{"no prefix", "app/main.go", ""},
		{"wrong label", "9~other/app/main.go", ""},
		{"label without separator slash", "1~feat", ""},
		{"unknown old path", "1~feat/app/new.go", "app/old.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.FileDiff(diff.FileDiffRequest{Path: tt.path, OldPath: tt.oldPath})
			require.Error(t, err)
			assert.ErrorIs(t, err, diff.ErrUnknownStackPath)
		})
	}
	assert.Empty(t, inner.FileDiffCalls(), "an unroutable path must never reach a level")
}

func TestStackRenderer_FileDiff_innerError(t *testing.T) {
	boom := errors.New("boom")
	inner := &mocks.RendererMock{
		FileDiffFunc: func(diff.FileDiffRequest) ([]diff.DiffLine, error) { return nil, boom },
	}
	s, err := diff.NewStackRenderer([]diff.StackLevel{{Base: "main", Head: "feat", Inner: inner}})
	require.NoError(t, err)

	_, err = s.FileDiff(diff.FileDiffRequest{Path: "1~feat/a.go"})
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "stack renderer, file diff 1~feat/a.go")
}

func TestStackRenderer_LabelOf(t *testing.T) {
	inner := echoRenderer("a.go")
	s, err := diff.NewStackRenderer([]diff.StackLevel{
		{Base: "main", Head: "feat-a", Inner: inner},
		{Base: "feat-a", Head: "feature/b", Inner: inner},
	})
	require.NoError(t, err)

	label, rest, ok := s.LabelOf("2~feature/b/app/main.go")
	require.True(t, ok)
	assert.Equal(t, "2~feature/b", label)
	assert.Equal(t, "app/main.go", rest)

	_, _, ok = s.LabelOf("app/main.go")
	assert.False(t, ok)
}

func TestStackRenderer_Levels(t *testing.T) {
	inner := echoRenderer("a.go")
	s, err := diff.NewStackRenderer([]diff.StackLevel{{Base: "main", Head: "feat", Inner: inner}})
	require.NoError(t, err)

	lvls := s.Levels()
	require.Len(t, lvls, 1)
	assert.Equal(t, "1~feat", lvls[0].Label())
	assert.Equal(t, "main..feat", lvls[0].Ref())

	lvls[0] = diff.StackLevel{}
	assert.Equal(t, "1~feat", s.Levels()[0].Label(), "Levels must return a copy")
}
