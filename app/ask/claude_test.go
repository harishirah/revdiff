package ask

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClaude logs each invocation's args and stdin, then answers in the shape
// FAKE_CLAUDE_MODE selects.
const fakeClaude = `#!/usr/bin/env bash
{ printf -- '--- call\n'; printf '%s\n' "$@"; printf -- '--- stdin\n'; cat; printf '\n'; } >>"$FAKE_CLAUDE_LOG"
init='{"type":"system","subtype":"init","session_id":"x"}'
case "$FAKE_CLAUDE_MODE" in
stream)
    echo 'not json: a warning line'
    echo "$init"
    echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hmm"}}}'
    echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"The lock "}}}'
    echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"guards the cache."}}}'
    echo '{"type":"result","subtype":"success","is_error":false,"result":"The lock guards the cache."}'
    ;;
result-only)
    echo "$init"
    echo '{"type":"result","subtype":"success","is_error":false,"result":"whole answer"}'
    ;;
error)
    echo "$init"
    echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"rate limited"}'
    ;;
crash)
    echo 'fatal: not logged in' >&2
    exit 3
    ;;
hang)
    echo "$init"
    sleep 30
    ;;
esac
`

func newFake(t *testing.T, mode string) (*Claude, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake claude is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(bin, []byte(fakeClaude), 0o700)) //nolint:gosec // test fixture must be executable
	log := filepath.Join(dir, "calls.log")
	t.Setenv("FAKE_CLAUDE_LOG", log)
	t.Setenv("FAKE_CLAUDE_MODE", mode)
	c, err := New(Options{Bin: bin, WorkDir: dir, Model: "haiku", Context: "Diff: main..feat-auth"})
	require.NoError(t, err)
	return c, log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log) //nolint:gosec // test-owned temp file
	require.NoError(t, err)
	parts := strings.Split(string(b), "--- call\n")
	return parts[1:]
}

func collect(t *testing.T, c *Claude, prompt string) (string, error) {
	t.Helper()
	var got strings.Builder
	err := c.Ask(context.Background(), prompt, func(s string) { got.WriteString(s) })
	return got.String(), err
}

func TestClaude_streamsAndResumesTheSession(t *testing.T) {
	c, log := newFake(t, "stream")

	answer, err := collect(t, c, "why the lock?")
	require.NoError(t, err)
	assert.Equal(t, "The lock guards the cache.", answer, "text deltas only, never the result repeated")

	_, err = collect(t, c, "and the defer?")
	require.NoError(t, err)

	got := calls(t, log)
	require.Len(t, got, 2)
	first, second := got[0], got[1]
	assert.Contains(t, first, "--session-id\n"+c.SessionID()+"\n")
	assert.NotContains(t, first, "--resume")
	assert.Contains(t, second, "--resume\n"+c.SessionID()+"\n")
	assert.NotContains(t, second, "--session-id")

	assert.Contains(t, first, "--- stdin\nwhy the lock?")
	assert.Contains(t, second, "--- stdin\nand the defer?")
	for _, want := range []string{
		"-p\n", "--output-format\nstream-json\n", "--include-partial-messages\n",
		"--permission-mode\nplan\n", "--model\nhaiku\n",
		"--allowedTools\nRead\nGrep\nGlob\nBash(git show:*)\n",
		"--disallowedTools\nEdit\nWrite\nNotebookEdit\nExitPlanMode\n",
		"Review context:\nDiff: main..feat-auth\n",
	} {
		assert.Contains(t, first, want)
	}
}

func TestClaude_resultOnlyAnswer(t *testing.T) {
	c, _ := newFake(t, "result-only")
	answer, err := collect(t, c, "q")
	require.NoError(t, err)
	assert.Equal(t, "whole answer", answer)
}

func TestClaude_errorResult(t *testing.T) {
	c, log := newFake(t, "error")
	_, err := collect(t, c, "q")
	require.ErrorContains(t, err, "rate limited")

	// the session was created before the failure, so the next turn resumes it
	_, _ = collect(t, c, "again")
	assert.Contains(t, calls(t, log)[1], "--resume\n")
}

func TestClaude_crashReportsStderr(t *testing.T) {
	c, log := newFake(t, "crash")
	_, err := collect(t, c, "q")
	require.ErrorContains(t, err, "not logged in")

	// no init event: the session was never created, so retrying starts it again
	_, _ = collect(t, c, "again")
	assert.Contains(t, calls(t, log)[1], "--session-id\n")
}

func TestClaude_closeStopsAQuestionInFlight(t *testing.T) {
	c, log := newFake(t, "hang")
	done := make(chan error, 1)
	go func() { done <- c.Ask(context.Background(), "q", func(string) {}) }()

	// wait until the process is running and parked in its sleep, so Close has
	// to stop a live process rather than one that never started
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(log) //nolint:gosec // test-owned temp file
		return err == nil && strings.Contains(string(b), "--- stdin\nq")
	}, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	started := time.Now()
	c.Close()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
		assert.Less(t, time.Since(started), 5*time.Second, "the sleeping child must not keep Ask waiting")
	case <-time.After(10 * time.Second):
		t.Fatal("Ask did not return after Close")
	}
}

func TestNewSessionID_isUUIDv4(t *testing.T) {
	id, err := newSessionID()
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`), id)
}
