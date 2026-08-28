package diff

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// StackLabelSeparator splits the ordinal from the branch name in a synthetic
// stack label. Git's check-ref-format forbids "~" in branch names, so the split
// is unambiguous for any legal branch and any stack size.
const StackLabelSeparator = "~"

// ErrUnknownStackPath is returned when a path does not carry a known stack label.
var ErrUnknownStackPath = errors.New("path does not belong to any stack level")

// StackLevel is one PR in a stack: the diff of Head against Base, rendered by
// Inner. Label is derived, not supplied — see NewStackRenderer.
type StackLevel struct {
	Base  string   // parent branch, the left side of the diff range
	Head  string   // this PR's branch, the right side and the source of Label
	Inner Renderer // per-level renderer, already wrapped with any include/exclude filters

	label string // "<ordinal><sep><head>", assigned by NewStackRenderer
}

// Label returns the synthetic path prefix for this level, e.g. "01~feat-auth".
func (l StackLevel) Label() string { return l.label }

// Ref returns the combined ref range this level diffs, e.g. "main..feat-auth".
func (l StackLevel) Ref() string { return l.Base + ".." + l.Head }

// StackRenderer presents several ref ranges as a single flat file list so a whole
// PR stack can be reviewed in one session. Paths are prefixed with a synthetic
// per-level label ("01~feat-auth/app/main.go") which keeps files from different
// levels distinct in the file tree, the annotation store, and the annotation
// output, where the bare path would otherwise collide. FileDiff strips the prefix
// and routes to the level that owns it.
//
// The levels are ordered bottom-up: index 0 is the PR closest to the trunk. The
// ordinal in each label is zero-padded to a uniform width so that a lexical sort
// of the prefixed paths — which is what the file tree does — reproduces stack
// order rather than alphabetical branch order.
type StackRenderer struct {
	levels []StackLevel
	byLen  []StackLevel // levels sorted by descending label length, for prefix stripping
}

// NewStackRenderer creates a renderer over the given levels, which must be
// ordered bottom-up (trunk-most first). Labels are derived from each level's
// Head so that a label is always a valid checkout target for the consuming agent.
func NewStackRenderer(levels []StackLevel) (*StackRenderer, error) {
	if len(levels) == 0 {
		return nil, errors.New("stack renderer: no levels")
	}

	width := len(strconv.Itoa(len(levels)))
	seen := make(map[string]bool, len(levels))
	out := make([]StackLevel, 0, len(levels))
	for i, lv := range levels {
		switch {
		case lv.Head == "":
			return nil, fmt.Errorf("stack renderer: level %d has no head", i+1)
		case lv.Base == "":
			return nil, fmt.Errorf("stack renderer: level %d (%s) has no base", i+1, lv.Head)
		case lv.Inner == nil:
			return nil, fmt.Errorf("stack renderer: level %d (%s) has no renderer", i+1, lv.Head)
		case seen[lv.Head]:
			return nil, fmt.Errorf("stack renderer: duplicate head %q", lv.Head)
		}
		seen[lv.Head] = true
		lv.label = fmt.Sprintf("%0*d%s%s", width, i+1, StackLabelSeparator, lv.Head)
		out = append(out, lv)
	}

	byLen := make([]StackLevel, len(out))
	copy(byLen, out)
	sort.SliceStable(byLen, func(i, j int) bool { return len(byLen[i].label) > len(byLen[j].label) })

	return &StackRenderer{levels: out, byLen: byLen}, nil
}

// Levels returns the resolved levels in stack order, labels included.
func (s *StackRenderer) Levels() []StackLevel {
	out := make([]StackLevel, len(s.levels))
	copy(out, s.levels)
	return out
}

// Labels returns the synthetic level labels in stack order.
func (s *StackRenderer) Labels() []string {
	out := make([]string, 0, len(s.levels))
	for _, lv := range s.levels {
		out = append(out, lv.label)
	}
	return out
}

// ChangedFiles concatenates the changed files of every level, prefixing each path
// with that level's label. The ref and staged arguments are ignored: each level
// carries its own range, and a staged diff is meaningless against a ref range.
func (s *StackRenderer) ChangedFiles(_ string, _ bool) ([]FileEntry, error) {
	var all []FileEntry
	for _, lv := range s.levels {
		entries, err := lv.Inner.ChangedFiles(lv.Ref(), false)
		if err != nil {
			return nil, fmt.Errorf("stack renderer, changed files for %s: %w", lv.Ref(), err)
		}
		for _, e := range entries {
			e.Path = lv.label + "/" + e.Path
			if e.OldPath != "" {
				e.OldPath = lv.label + "/" + e.OldPath
			}
			all = append(all, e)
		}
	}
	return all, nil
}

// FileDiff routes the request to the level owning req.Path, with the label
// prefix stripped from both Path and OldPath.
//
// Staged is forced false: the caller retries added files with Staged=true (see
// Model.fetchEffectiveFileDiff), and "git diff --cached <a>..<b>" is a usage
// error, so every added file in the stack would fail to load without this.
func (s *StackRenderer) FileDiff(req FileDiffRequest) ([]DiffLine, error) {
	lv, path, ok := s.route(req.Path)
	if !ok {
		return nil, fmt.Errorf("stack renderer, file diff %s: %w", req.Path, ErrUnknownStackPath)
	}

	inner := req
	inner.Ref = lv.Ref()
	inner.Path = path
	inner.Staged = false
	if req.OldPath != "" {
		old, stripped := stripStackLabel(req.OldPath, lv.label)
		if !stripped {
			return nil, fmt.Errorf("stack renderer, file diff %s: old path %s: %w", req.Path, req.OldPath, ErrUnknownStackPath)
		}
		inner.OldPath = old
	}

	lines, err := lv.Inner.FileDiff(inner)
	if err != nil {
		return nil, fmt.Errorf("stack renderer, file diff %s: %w", req.Path, err)
	}
	return lines, nil
}

// LabelOf returns the stack label owning path, and the path with that label
// stripped. Used by callers that need to attribute a path to a level without
// running a diff.
func (s *StackRenderer) LabelOf(path string) (label, rest string, ok bool) {
	lv, rest, ok := s.route(path)
	if !ok {
		return "", "", false
	}
	return lv.label, rest, true
}

// route finds the level whose label prefixes path. Matching is by exact label,
// longest first, never by splitting on the first "/" — branch names may contain
// slashes, so "02~feature/auth/app/main.go" cannot be split positionally.
func (s *StackRenderer) route(path string) (lv StackLevel, rest string, ok bool) {
	for _, candidate := range s.byLen {
		if rest, stripped := stripStackLabel(path, candidate.label); stripped {
			return candidate, rest, true
		}
	}
	return StackLevel{}, "", false
}

// stripStackLabel removes the "<label>/" prefix from path.
func stripStackLabel(path, label string) (rest string, ok bool) {
	return strings.CutPrefix(path, label+"/")
}
