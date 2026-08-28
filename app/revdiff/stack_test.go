package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateStackFlags_parse(t *testing.T) {
	tests := []struct {
		name string
		refs []string
		want []stackRef
	}{
		{"none", nil, nil},
		{"single", []string{"main..feat"}, []stackRef{{base: "main", head: "feat"}}},
		{"three levels keep flag order", []string{"main..a", "a..b", "b..c"}, []stackRef{
			{base: "main", head: "a"}, {base: "a", head: "b"}, {base: "b", head: "c"},
		}},
		{"slash in branch names", []string{"main..feature/auth"}, []stackRef{{base: "main", head: "feature/auth"}}},
		{"surrounding whitespace trimmed", []string{"  main..feat  "}, []stackRef{{base: "main", head: "feat"}}},
		{"sha refs", []string{"abc123..def456"}, []stackRef{{base: "abc123", head: "def456"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := options{StackRefs: tt.refs}
			got, err := validateStackFlags(opts)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateStackFlags_parseErrors(t *testing.T) {
	tests := []struct {
		name   string
		refs   []string
		errMsg string
	}{
		{"empty value", []string{""}, "empty value"},
		{"no separator", []string{"main"}, "expected BASE..HEAD"},
		{"missing base", []string{"..feat"}, "both BASE and HEAD are required"},
		{"missing head", []string{"main.."}, "both BASE and HEAD are required"},
		{"triple dot", []string{"main...feat"}, "not triple-dot"},
		{"two ranges", []string{"main..a..b"}, "exactly one BASE..HEAD pair"},
		{"duplicate head", []string{"main..a", "b..a"}, `duplicate head "a"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateStackFlags(options{StackRefs: tt.refs})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestValidateStackFlags_conflicts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*options)
		errMsg string
	}{
		{"positional base", func(o *options) { o.Refs.Base = "main" }, "refs"},
		{"positional against", func(o *options) { o.Refs.Against = "feat" }, "refs"},
		{"staged", func(o *options) { o.Staged = true }, "--staged"},
		{"untracked", func(o *options) { o.Untracked = true }, "--untracked"},
		{"only", func(o *options) { o.Only = []string{"a.go"} }, "--only"},
		{"all-files", func(o *options) { o.AllFiles = true }, "--all-files"},
		{"stdin", func(o *options) { o.Stdin = true }, "--stdin"},
		{"compare", func(o *options) { o.CompareOld = "a" }, "--compare-old/--compare-new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := options{StackRefs: []string{"main..feat"}}
			tt.mutate(&opts)
			_, err := validateStackFlags(opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

// include/exclude apply per level (inside the stack renderer) and --annotations
// round-trips through the renderer, so both stay compatible with stack mode.
func TestValidateStackFlags_allowedCombinations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*options)
	}{
		{"include", func(o *options) { o.Include = []string{"app"} }},
		{"exclude", func(o *options) { o.Exclude = []string{"vendor"} }},
		{"annotations", func(o *options) { o.Annotations = "/tmp/a.md" }},
		{"output", func(o *options) { o.Output = "/tmp/out.md" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := options{StackRefs: []string{"main..feat"}}
			tt.mutate(&opts)
			got, err := validateStackFlags(opts)
			require.NoError(t, err)
			assert.Len(t, got, 1)
		})
	}
}

func TestParseArgs_stackRefs(t *testing.T) {
	opts, err := parseArgs([]string{"--stack-ref=main..feat-a", "--stack-ref=feat-a..feat-b"})
	require.NoError(t, err)
	assert.True(t, opts.stackMode())
	assert.Equal(t, []stackRef{{base: "main", head: "feat-a"}, {base: "feat-a", head: "feat-b"}}, opts.stackLevels)
}

func TestParseArgs_stackRefConflict(t *testing.T) {
	_, err := parseArgs([]string{"--stack-ref=main..feat", "--staged"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--stack-ref cannot be used with --staged")
}

func TestOptions_stackMode(t *testing.T) {
	assert.False(t, options{}.stackMode())
	assert.True(t, options{stackLevels: []stackRef{{base: "main", head: "a"}}}.stackMode())
}
