package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// the launcher switches the Claude side panel on through env when claude is
// installed, so an older revdiff binary that lacks --ask is never broken.
func TestLauncherEnablesAskPanel(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell launchers are not used on windows")
	}
	root := testRepoRoot(t)
	launchers := map[string]string{
		"claude": ".claude-plugin/skills/revdiff/scripts/launch-revdiff.sh",
		"codex":  "plugins/codex/skills/revdiff/scripts/launch-revdiff.sh",
	}
	cases := []struct {
		name       string
		withClaude bool
		optOut     bool
		want       func(binDir string) string
	}{
		{name: "claude installed", withClaude: true, want: func(binDir string) string { return "true|" + filepath.Join(binDir, "claude") }},
		{name: "opted out", withClaude: true, optOut: true, want: func(string) string { return "0|missing" }},
		{name: "no claude", want: func(string) string { return "missing|missing" }},
	}
	for name, path := range launchers {
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				backend := launcherBackend{name: "tmux", command: "tmux", env: map[string]string{"TMUX": "1"}}
				env := fakeLauncherEnv(t, launcherRun{backend: backend})
				binDir := filepath.SplitList(env["PATH"])[0]
				env["REVDIFF_ASK"] = ""
				if tc.withClaude {
					writeExecutable(t, filepath.Join(binDir, "claude"), "#!/bin/sh\n")
				} else {
					// keep a user's real claude off PATH
					env["PATH"] = binDir + string(os.PathListSeparator) + "/usr/bin:/bin"
				}
				if tc.optOut {
					env["REVDIFF_ASK"] = "0"
				}
				askFile := filepath.Join(env["TMPDIR"], "ask-env")
				env["FAKE_ASK_FILE"] = askFile

				res := runTestCmd(t, cmdReq{dir: root, name: "bash", args: []string{filepath.Join(root, path)}, env: env})
				require.Equal(t, 0, res.code, "stderr: %s", res.stderr)
				assertFileContent(t, askFile, tc.want(binDir))
			})
		}
	}
}
