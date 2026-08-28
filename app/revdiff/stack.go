package main

import (
	"errors"
	"fmt"
	"strings"
)

// stackRef is one parsed --stack-ref entry: the diff range of a single PR in a
// stack. Levels are kept in the order the flags were given, bottom-up.
type stackRef struct {
	base string
	head string
}

// validateStackFlags parses --stack-ref entries and checks them against the
// flags stack mode cannot combine with. Returns the levels in flag order so
// callers stash them on opts and resolution runs once.
//
// Conflict checks run before parse checks so a misuse error is not masked by a
// syntax error.
func validateStackFlags(opts options) ([]stackRef, error) {
	if len(opts.StackRefs) == 0 {
		return nil, nil
	}
	conflicts := []struct {
		bad  bool
		flag string
	}{
		{opts.Refs.Base != "" || opts.Refs.Against != "", "refs"},
		{opts.Staged, "--staged"},
		{opts.Untracked, "--untracked"},
		{len(opts.Only) > 0, "--only"},
		{opts.AllFiles, "--all-files"},
		{opts.Stdin, "--stdin"},
		{opts.CompareOld != "" || opts.CompareNew != "", "--compare-old/--compare-new"},
	}
	for _, c := range conflicts {
		if c.bad {
			return nil, fmt.Errorf("--stack-ref cannot be used with %s", c.flag)
		}
	}

	levels := make([]stackRef, 0, len(opts.StackRefs))
	seen := make(map[string]bool, len(opts.StackRefs))
	for _, raw := range opts.StackRefs {
		lv, err := parseStackRef(raw)
		if err != nil {
			return nil, err
		}
		if seen[lv.head] {
			return nil, fmt.Errorf("--stack-ref: duplicate head %q", lv.head)
		}
		seen[lv.head] = true
		levels = append(levels, lv)
	}
	return levels, nil
}

// parseStackRef splits a "BASE..HEAD" entry. Triple-dot is rejected: its
// merge-base semantics contradict "diff against the recorded parent tip",
// which is what a stack level means.
func parseStackRef(raw string) (stackRef, error) {
	spec := strings.TrimSpace(raw)
	if spec == "" {
		return stackRef{}, errors.New("--stack-ref: empty value")
	}
	if strings.Contains(spec, "...") {
		return stackRef{}, fmt.Errorf("--stack-ref %q: use BASE..HEAD, not triple-dot", raw)
	}
	base, head, found := strings.Cut(spec, "..")
	if !found {
		return stackRef{}, fmt.Errorf("--stack-ref %q: expected BASE..HEAD", raw)
	}
	if base == "" || head == "" {
		return stackRef{}, fmt.Errorf("--stack-ref %q: both BASE and HEAD are required", raw)
	}
	if strings.Contains(head, "..") {
		return stackRef{}, fmt.Errorf("--stack-ref %q: expected exactly one BASE..HEAD pair", raw)
	}
	return stackRef{base: base, head: head}, nil
}

// stackMode reports whether this invocation is a stack review.
func (o options) stackMode() bool { return len(o.stackLevels) > 0 }
