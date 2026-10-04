package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGhReviewScript serves `gh pr view`, `gh pr diff` and the review-creating
// `gh api` call. The api call records its stdin payload and fails with
// FAKE_GH_API_ERROR when that is set.
const fakeGhReviewScript = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_GH_LOG"
case "$1 $2" in
"pr view") printf '{"headRefOid":"abc123","url":"https://github.com/o/r/pull/%s"}\n' "$3" ;;
"pr diff") cat "$FAKE_GH_DIFF" ;;
"api -X")
    cat >"$FAKE_GH_PAYLOAD"
    if [ -n "${FAKE_GH_API_ERROR:-}" ]; then echo "$FAKE_GH_API_ERROR" >&2; exit 1; fi
    printf '{"html_url":"https://github.com/o/r/pull/7#pullrequestreview-1"}\n' ;;
*) echo "fake gh: unexpected: $*" >&2; exit 2 ;;
esac
`

// reviewTestDiff has two hunks in app/main.go and a deleted file. Line numbers:
// hunk 1 old 10-14 / new 10-16 (old 12 removed, new 12-14 added); hunk 2 old
// 40-42 / new 42-45 (new 43 added); old.txt old 1-2 removed.
const reviewTestDiff = `diff --git a/app/main.go b/app/main.go
index 1111111..2222222 100644
--- a/app/main.go
+++ b/app/main.go
@@ -10,5 +10,7 @@ func main() {
 	a := 1
 	b := 2
-	c := 3
+	c := 30
+	d := 4
+	e := 5
 	f := 6
 	g := 7
@@ -40,3 +42,4 @@ func other() {
 	x := 1
+	y := 2
 	z := 3
 	w := 4
diff --git a/old.txt b/old.txt
deleted file mode 100644
index 3333333..0000000
--- a/old.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-gone1
-gone2
`

const reviewTestAnnotations = `## app/main.go (file-level)
overall: split this file

## app/main.go:12 (-)
why remove c

## app/main.go:12-14 (+)
these three should be a struct

## app/main.go:43 (+)
what is y ??

## app/main.go:44 (+)
z needs a comment
 ## not a header, escaped

## app/main.go:200 (+)
outside the diff

## app/main.go:14-43 (+)
spans two hunks

## old.txt:2 (-)
was this used?
`

type reviewFixture struct {
	dir string
	env map[string]string
}

func newReviewFixture(t *testing.T, annotations string, env map[string]string) reviewFixture {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "annotations.md"), annotations)
	writeTestFile(t, filepath.Join(dir, "pr.diff"), reviewTestDiff)
	writeExecutable(t, filepath.Join(dir, "bin", "gh"), fakeGhReviewScript)
	fullEnv := map[string]string{
		"PATH":            filepath.Join(dir, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_GH_LOG":     filepath.Join(dir, "gh.log"),
		"FAKE_GH_DIFF":    filepath.Join(dir, "pr.diff"),
		"FAKE_GH_PAYLOAD": filepath.Join(dir, "payload.json"),
	}
	for k, v := range env {
		fullEnv[k] = v
	}
	return reviewFixture{dir: dir, env: fullEnv}
}

func (f reviewFixture) run(t *testing.T, args ...string) cmdResult {
	t.Helper()
	script := filepath.Join(testRepoRoot(t), ".claude-plugin", "skills", "revdiff", "scripts", "post-gh-review.py")
	full := append([]string{script, "--annotations", filepath.Join(f.dir, "annotations.md")}, args...)
	return runTestCmd(t, cmdReq{dir: f.dir, name: python3Path(t), args: full, env: f.env})
}

func (f reviewFixture) ghLog(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "gh.log"))
	require.NoError(t, err)
	return string(b)
}

func TestPostGhReview(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("skill scripts are not used on windows")
	}

	t.Run("posts a draft review with mapped comments", func(t *testing.T) {
		t.Parallel()
		f := newReviewFixture(t, reviewTestAnnotations, nil)
		res := f.run(t, "--pr", "7")
		require.Equal(t, 0, res.code, "stderr: %s", res.stderr)

		assert.Contains(t, res.stdout, "inline_comments: 4\n")
		assert.Contains(t, res.stdout, "body_notes: 3\n")
		assert.Contains(t, res.stdout, "skipped_questions: 1\n")
		assert.Contains(t, res.stdout, "review_url: https://github.com/o/r/pull/7#pullrequestreview-1\n")
		assert.Contains(t, f.ghLog(t), "api -X POST repos/o/r/pulls/7/reviews --input -\n")

		want := map[string]any{
			"commit_id": "abc123",
			"body": "**`app/main.go (file-level)`**\noverall: split this file\n\n" +
				"**`app/main.go:200`**\noutside the diff\n\n" +
				"**`app/main.go:14-43`**\nspans two hunks",
			"comments": []map[string]any{
				{"path": "app/main.go", "side": "LEFT", "line": 12, "body": "why remove c"},
				{"path": "app/main.go", "side": "RIGHT", "start_line": 12, "start_side": "RIGHT", "line": 14,
					"body": "these three should be a struct"},
				{"path": "app/main.go", "side": "RIGHT", "line": 44, "body": "z needs a comment\n## not a header, escaped"},
				{"path": "old.txt", "side": "LEFT", "line": 2, "body": "was this used?"},
			},
		}
		wantJSON, err := json.Marshal(want)
		require.NoError(t, err)
		got := readRepoFile(t, f.dir, "payload.json")
		assert.JSONEq(t, string(wantJSON), got, "no event key: the review stays a pending draft")
	})

	t.Run("dry run previews and posts nothing", func(t *testing.T) {
		t.Parallel()
		f := newReviewFixture(t, reviewTestAnnotations, nil)
		res := f.run(t, "--pr", "7", "--repo", "up/stream", "--dry-run")
		require.Equal(t, 0, res.code, "stderr: %s", res.stderr)

		assert.Contains(t, res.stdout, "posted: false (dry run)\n")
		assert.Contains(t, res.stdout, "RIGHT app/main.go:12-14  these three should be a struct\n")
		assert.Contains(t, res.stdout, "LEFT  old.txt:2  was this used?\n")
		assert.Contains(t, res.stdout, "SKIP  app/main.go:43  what is y ??\n")
		assert.Contains(t, res.stdout, "BODY  app/main.go:200  (outside the PR diff) outside the diff\n")
		assert.Contains(t, res.stdout, "BODY  app/main.go (file-level)  overall: split this file\n")

		log := f.ghLog(t)
		assert.Contains(t, log, "pr view 7 --json headRefOid,url --repo up/stream\n")
		assert.Contains(t, log, "pr diff 7 --repo up/stream\n")
		assert.NotContains(t, log, "api ")
		assert.NoFileExists(t, filepath.Join(f.dir, "payload.json"))
	})

	t.Run("prefix keeps one stack level and strips its label", func(t *testing.T) {
		t.Parallel()
		annotations := "## 1~feat/a/app/main.go:44 (+)\nlevel one\n\n## 2~feat/b/app/main.go:12 (-)\nlevel two\n"
		f := newReviewFixture(t, annotations, nil)
		res := f.run(t, "--pr", "7", "--prefix", "1~feat/a/")
		require.Equal(t, 0, res.code, "stderr: %s", res.stderr)

		var payload struct {
			Comments []map[string]any `json:"comments"`
		}
		require.NoError(t, json.Unmarshal([]byte(readRepoFile(t, f.dir, "payload.json")), &payload))
		require.Len(t, payload.Comments, 1)
		assert.Equal(t, "app/main.go", payload.Comments[0]["path"])
		assert.Equal(t, "level one", payload.Comments[0]["body"])
	})

	t.Run("explains an existing draft review", func(t *testing.T) {
		t.Parallel()
		f := newReviewFixture(t, reviewTestAnnotations, map[string]string{
			"FAKE_GH_API_ERROR": "gh: User can only have one pending review per pull request (HTTP 422)",
		})
		res := f.run(t, "--pr", "7")
		assert.Equal(t, 1, res.code)
		assert.Contains(t, res.stderr, "already have a draft review on this PR")
	})

	t.Run("posts nothing when only questions remain", func(t *testing.T) {
		t.Parallel()
		f := newReviewFixture(t, "## app/main.go:44 (+)\nexplain z\n", nil)
		res := f.run(t, "--pr", "7")
		require.Equal(t, 0, res.code, "stderr: %s", res.stderr)
		assert.Contains(t, res.stdout, "posted: false (nothing to post)\n")
		assert.NotContains(t, f.ghLog(t), "api ")
	})
}
