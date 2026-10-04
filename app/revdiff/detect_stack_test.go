package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stackPR is one open pull request served by the fake gh.
type stackPR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Head   string `json:"headRefName"`
	Base   string `json:"baseRefName"`
	URL    string `json:"url"`
	Cross  bool   `json:"isCrossRepository"`
}

// fakeGhScript answers `gh auth status` and `gh pr list` filtered by --head or
// --base from per-branch JSON fixtures, applying the caller's --jq with jq so the
// script's real filters run. An unfiltered `gh pr list` exits non-zero: listing
// every open PR and filtering locally is what missed stacks in large repos.
const fakeGhScript = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_GH_LOG"
if [ "$#" -ge 2 ] && [ "$1 $2" = "auth status" ]; then exit 0; fi
if [ "$#" -lt 2 ] || [ "$1 $2" != "pr list" ]; then echo "fake gh: unexpected: $*" >&2; exit 2; fi
shift 2
key="" value="" filter="."
while [ "$#" -gt 0 ]; do
    case "$1" in
    --head) key=head; value="$2"; shift ;;
    --base) key=base; value="$2"; shift ;;
    --jq) filter="$2"; shift ;;
    esac
    shift
done
if [ -z "$key" ]; then echo "fake gh: unfiltered pr list" >&2; exit 3; fi
fixture="$FAKE_GH_DIR/$key/$(printf '%s' "$value" | tr '/' '%').json"
if [ -f "$fixture" ]; then jq -r "$filter" "$fixture"; else echo '[]' | jq -r "$filter"; fi
`

type stackFixture struct {
	dir string            // git repo the script runs in
	env map[string]string // fake gh on PATH plus its fixture and log locations
}

// newStackFixture creates a repo with a main branch plus the given local
// branches, checks out checkout, and serves prs through the fake gh.
func newStackFixture(t *testing.T, local []string, checkout string, prs []stackPR) stackFixture {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not found")
	}

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	runMainTestGit(t, repo, "init", "-q", "-b", "main")
	runMainTestGit(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false",
		"commit", "-q", "--allow-empty", "-m", "init")
	for _, b := range local {
		runMainTestGit(t, repo, "branch", b)
	}
	runMainTestGit(t, repo, "switch", "-q", checkout)

	byFilter := make(map[string][]stackPR)
	for _, pr := range prs {
		pr.URL = "https://github.com/o/r/pull/" + strconv.Itoa(pr.Number)
		byFilter["head/"+pr.Head] = append(byFilter["head/"+pr.Head], pr)
		byFilter["base/"+pr.Base] = append(byFilter["base/"+pr.Base], pr)
	}
	ghDir := filepath.Join(tmp, "gh")
	for key, list := range byFilter {
		kind, branch, _ := strings.Cut(key, "/")
		b, err := json.Marshal(list)
		require.NoError(t, err)
		writeTestFile(t, filepath.Join(ghDir, kind, strings.ReplaceAll(branch, "/", "%")+".json"), string(b))
	}

	bin := filepath.Join(tmp, "bin")
	writeExecutable(t, filepath.Join(bin, "gh"), fakeGhScript)
	return stackFixture{dir: repo, env: map[string]string{
		"PATH":        bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_GH_DIR": ghDir,
		"FAKE_GH_LOG": filepath.Join(tmp, "gh.log"),
	}}
}

// run executes detect-stack.sh and parses its key: value output.
func (f stackFixture) run(t *testing.T, args ...string) map[string]string {
	t.Helper()
	script := filepath.Join(testRepoRoot(t), ".claude-plugin", "skills", "revdiff", "scripts", "detect-stack.sh")
	res := runTestCmd(t, cmdReq{dir: f.dir, name: "bash", args: append([]string{script}, args...), env: f.env})
	require.Equal(t, 0, res.code, "stderr: %s", res.stderr)

	out := make(map[string]string)
	for line := range strings.SplitSeq(strings.TrimRight(res.stdout, "\n"), "\n") {
		key, value, ok := strings.Cut(line, ":")
		require.True(t, ok, "malformed output line %q", line)
		out[key] = strings.TrimPrefix(value, " ")
	}
	return out
}

func TestDetectStack(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("skill scripts are not used on windows")
	}

	linear := []stackPR{
		{Number: 11, Title: "auth", Head: "feat/auth", Base: "main"},
		{Number: 12, Title: "ui \x1b[31mred", Head: "feat/ui", Base: "feat/auth"},
		{Number: 13, Title: "docs", Head: "feat/docs", Base: "feat/ui"},
	}
	allLocal := []string{"feat/auth", "feat/ui", "feat/docs"}

	t.Run("walks down and up from the middle of a linear stack", func(t *testing.T) {
		t.Parallel()
		out := newStackFixture(t, allLocal, "feat/ui", linear).run(t)
		assert.Equal(t, "true", out["stack_ok"])
		assert.Empty(t, out["error"])
		assert.Equal(t, "false", out["needs_ask"])
		assert.Equal(t, "main", out["trunk"])
		assert.Equal(t, "3", out["stack_len"])
		assert.Equal(t, "2", out["current_level"])
		assert.Equal(t, "feat/ui", out["start_branch"])
		assert.Equal(t, "main..feat/auth", out["level_1_ref"])
		assert.Equal(t, "feat/auth..feat/ui", out["level_2_ref"])
		assert.Equal(t, "feat/ui..feat/docs", out["level_3_ref"])
		assert.Equal(t, "11", out["level_1_pr"])
		assert.Equal(t, "https://github.com/o/r/pull/13", out["level_3_url"])
		assert.Equal(t, "ui [31mred", out["level_2_title"], "control characters are stripped")
		assert.Equal(t, "true", out["level_3_local"])
	})

	t.Run("ignores PRs from forks", func(t *testing.T) {
		t.Parallel()
		prs := append([]stackPR{
			{Number: 98, Title: "same head name", Head: "feat/auth", Base: "develop", Cross: true},
			{Number: 99, Title: "targets the top", Head: "contrib", Base: "feat/docs", Cross: true},
		}, linear...)
		out := newStackFixture(t, allLocal, "feat/auth", prs).run(t)
		assert.Equal(t, "true", out["stack_ok"])
		assert.Equal(t, "main", out["level_1_base"])
		assert.Equal(t, "11", out["level_1_pr"])
		assert.Equal(t, "3", out["stack_len"])
	})

	t.Run("stops at a fork and re-scopes to the chosen branch", func(t *testing.T) {
		t.Parallel()
		prs := append([]stackPR{}, linear...)
		prs = append(prs, stackPR{Number: 14, Title: "api", Head: "feat/api", Base: "feat/ui"})
		f := newStackFixture(t, append([]string{"feat/api"}, allLocal...), "feat/auth", prs)

		out := f.run(t)
		assert.Equal(t, "true", out["needs_ask"])
		assert.Equal(t, "feat/ui", out["fork_at"])
		assert.Equal(t, "feat/docs,feat/api", out["fork_candidates"])
		assert.Equal(t, "2", out["stack_len"])

		out = f.run(t, "feat/api")
		assert.Equal(t, "true", out["stack_ok"])
		assert.Equal(t, "false", out["needs_ask"])
		assert.Empty(t, out["fork_at"])
		assert.Equal(t, "feat/api", out["start_branch"])
		assert.Equal(t, "feat/auth", out["current_branch"])
		assert.Equal(t, "1", out["current_level"])
		assert.Equal(t, "3", out["stack_len"])
		assert.Equal(t, "feat/ui..feat/api", out["level_3_ref"])
	})

	t.Run("reports every missing branch in one fetch command", func(t *testing.T) {
		t.Parallel()
		out := newStackFixture(t, []string{"feat/ui"}, "feat/ui", linear).run(t)
		assert.Equal(t, "false", out["stack_ok"])
		assert.Equal(t, "branches not present locally: feat/auth, feat/docs "+
			"(run: git fetch origin feat/auth:feat/auth feat/docs:feat/docs)", out["error"])
		assert.Equal(t, "false", out["level_1_local"])
		assert.Equal(t, "true", out["level_2_local"])
	})

	t.Run("fails when the branch has no open PR", func(t *testing.T) {
		t.Parallel()
		out := newStackFixture(t, []string{"lonely"}, "lonely", linear).run(t)
		assert.Equal(t, "false", out["stack_ok"])
		assert.Equal(t, "no open PR found for branch lonely", out["error"])
		assert.Equal(t, "0", out["stack_len"])
		assert.Empty(t, out["trunk"])
	})

	t.Run("fails when the start branch is not local", func(t *testing.T) {
		t.Parallel()
		out := newStackFixture(t, allLocal, "feat/ui", linear).run(t, "feat/nope")
		assert.Equal(t, "false", out["stack_ok"])
		assert.Equal(t, "branch feat/nope not present locally (run: git fetch origin feat/nope:feat/nope)", out["error"])
	})

	t.Run("fails on a base cycle", func(t *testing.T) {
		t.Parallel()
		prs := []stackPR{
			{Number: 21, Title: "a", Head: "loop-a", Base: "loop-b"},
			{Number: 22, Title: "b", Head: "loop-b", Base: "loop-a"},
		}
		out := newStackFixture(t, []string{"loop-a", "loop-b"}, "loop-a", prs).run(t)
		assert.Equal(t, "false", out["stack_ok"])
		assert.Equal(t, "PR base cycle detected at loop-a", out["error"])
	})
}

// TestSkillScriptCopiesInSync pins the "keep in sync" header contract: every
// copied plugin script must equal the source named in its header, apart from
// the header line itself.
func TestSkillScriptCopiesInSync(t *testing.T) {
	t.Parallel()
	root := testRepoRoot(t)
	header := regexp.MustCompile(`(?m)^# source: (\S+) \(keep in sync\)\n`)

	checked := 0
	err := filepath.WalkDir(filepath.Join(root, "plugins"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".sh" {
			return err
		}
		body, err := os.ReadFile(path) //nolint:gosec // path comes from walking the repo
		require.NoError(t, err)
		m := header.FindSubmatch(body)
		if m == nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		require.NoError(t, err)
		want := readRepoFile(t, root, filepath.FromSlash(string(m[1])))
		assert.Equal(t, want, string(header.ReplaceAll(body, nil)), "%s drifted from %s", rel, m[1])
		checked++
		return nil
	})
	require.NoError(t, err)
	assert.Positive(t, checked, "no copied scripts found")
}
