package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupAsker(t *testing.T) {
	t.Run("off by default", func(t *testing.T) {
		asker, err := setupAsker(options{}, t.TempDir(), "")
		require.NoError(t, err)
		assert.Nil(t, asker)
	})

	t.Run("missing claude is an error", func(t *testing.T) {
		_, err := setupAsker(options{Ask: true, AskBin: filepath.Join(t.TempDir(), "claude")}, t.TempDir(), "")
		require.ErrorContains(t, err, "--ask: claude CLI not found")
	})

	t.Run("creates a conversation", func(t *testing.T) {
		bin := filepath.Join(t.TempDir(), "claude")
		require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // fixture must be executable
		asker, err := setupAsker(options{Ask: true, AskBin: bin}, t.TempDir(), "")
		require.NoError(t, err)
		require.NotNil(t, asker)
		assert.NotEmpty(t, asker.SessionID())
	})
}

func TestAskReviewContext(t *testing.T) {
	stack := options{stackLevels: []stackRef{{base: "main", head: "feat-a"}, {base: "feat-a", head: "feat-b"}}}
	got := askReviewContext(stack, "/repo", "")
	assert.Contains(t, got, "Repository: /repo\n")
	assert.Contains(t, got, "- PR 1: branch feat-a, diff main..feat-a\n- PR 2: branch feat-b, diff feat-a..feat-b\n")

	var ref options
	ref.Refs.Base = "main"
	assert.Contains(t, askReviewContext(ref, "/repo", ""), "Reviewing the diff for main.")

	got = askReviewContext(options{}, "/repo", "adds retry logic")
	assert.Contains(t, got, "Reviewing uncommitted changes")
	assert.Contains(t, got, "adds retry logic")
}
