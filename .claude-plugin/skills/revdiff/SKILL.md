---
name: revdiff
description: Review diffs, files, and documents with inline annotations in a TUI overlay, or answer questions about revdiff usage, configuration, themes, and keybindings. Opens revdiff in agterm/tmux/zellij/herdr/kitty/wezterm/cmux/ghostty/iterm2/emacs-vterm, captures annotations, and addresses them. Works in git, hg, and jj repos (auto-detected). Activates on "revdiff", "review diff", "review changes", "annotate diff", "git review with revdiff", "hg review with revdiff", "review jj change", "interactive diff review", "revdiff all files", "review all files", "browse all files", "revdiff <file>", "revdiff README.md", "revdiff /tmp/notes.txt", "review this file", "annotate this file", "review file with revdiff", "open this review in revdiff", "show review in revdiff", "review in revdiff", "revdiff config", "revdiff themes", "revdiff keybindings", "how to configure revdiff", "what themes does revdiff have". Reviews a whole stack of GitHub PRs in one session on "review the stack", "review my PR stack", "review all the stacked PRs".
argument-hint: 'optional: ref(s), "all files", or file path'
allowed-tools: [Bash, Read, Edit, Write, Grep, Glob]
---

# revdiff - TUI Diff Review

Review diffs with inline annotations using revdiff TUI in a terminal overlay. Works in git, hg, and jj repos (auto-detected).

## Activation Triggers

- "revdiff", "review diff", "review changes", "annotate diff"
- "revdiff HEAD~1", "revdiff main"
- "hg review with revdiff", "review jj change"
- "revdiff all files", "review all files", "browse all files"
- "revdiff all files exclude vendor"
- "revdiff README.md", "revdiff docs/plan.md", "revdiff /tmp/notes.txt" — single-file review (`--only` mode)
- "review this file", "annotate this file", "review file with revdiff"
- "open this review in revdiff", "show review in revdiff", "review in revdiff" — open an in-session review (preload mode)
- "review the stack", "review my PR stack", "review all the stacked PRs", "review the stack and fix everything" — stacked GitHub PR review (`--stack-ref` mode)

## Answering Questions

If the user asks a question about revdiff (configuration, themes, keybindings, installation, usage) rather than requesting a review session, consult the reference files in `references/` and answer directly. Do NOT launch the TUI for informational questions.

- `references/install.md` — installation methods and plugin setup
- `references/config.md` — config file, options, colors, chroma themes
- `references/usage.md` — examples, key bindings, output format

## Using Existing Review History

If the user says things like "locate my review", "use my latest revdiff annotations", "pull up the review I just did in another terminal", or "what did I annotate earlier" — the user ran revdiff outside this plugin flow and wants Claude to process the stored annotations. Read the most recent file from the persistent history directory via the helper script, then process the annotations through Step 3.5 classification as if they had come from a fresh launcher call:

```bash
${CLAUDE_SKILL_DIR}/scripts/read-latest-history.sh
```

The script resolves the history dir from `$REVDIFF_HISTORY_DIR` (default `~/.config/revdiff/history`), finds the repo subdir via VCS root basename (jj/git/hg), and prints the newest `.md` file found. Each history file contains a header (path, refs, and — when available — a git commit hash), the annotations in `## file:line (type)` format, and the raw git diff for annotated files. The `commit:` line and diff block are captured from git only; in hg/jj repos the diff block will be empty and no commit hash is recorded. See `references/usage.md` "Review History" section for directory layout, stdin/only handling, and override options.

The history file is also the recovery path when a review is cut short by a lost connection: on signal termination (a SIGHUP from a dropped SSH/tmux client, or a SIGTERM) revdiff saves the current annotations to history — never the `-o` output — so `read-latest-history.sh` still recovers them.

## Opening an In-Session Review

When the user asks to open an in-session review in revdiff (the conversation already contains review comments produced earlier in the session), write those comments to a temp file (e.g. `/tmp/revdiff-review-XXXXXX.md`) using the format documented in `references/usage.md` ("Output Format" section), then run the normal launcher flow (Step 1 ref detection, Step 2 invocation) with `--annotations=<temp-path>` appended. Step 3 onward handles the curated annotations as usual.

## Reviewing a Stack of GitHub PRs

When the user asks to "review the stack", "review my PR stack", "review all the stacked PRs", or "review this stack and fix everything", review every PR in one revdiff session instead of looping one PR at a time.

Stack mode is **git only** and requires the `gh` CLI, authenticated.

### Discover the stack

```bash
"${CLAUDE_SKILL_DIR}/scripts/detect-stack.sh"
```

It emits flat `key: value` lines. Read `stack_ok` first:

- `stack_ok: true` — use `level_N_ref` for each level, in ascending N (bottom-of-stack first).
- `stack_ok: false` — report `error:` verbatim; it is written to be actionable (gh missing, not authenticated, branches not checked out locally). When branches are missing it names all of them with the one `git fetch` command that creates them; offer to run it, then re-run detection. Do not work around it silently.
- `needs_ask: true` with `fork_at:` set — two open PRs share a base, so the stack is not linear. Ask the user which branch of the fork to review using the names in `fork_candidates`, then re-run `detect-stack.sh` with the chosen branch as its only argument; the walk then follows that line of the stack to its top.

Keep the whole table. The mapping from **level ordinal → branch** is what routes fixes later, and you already have it here.

### Launch

Pass one `--stack-ref` per level, in ascending order:

```bash
"$("${CLAUDE_SKILL_DIR}/scripts/resolve-launcher.sh" launch-revdiff.sh "${CLAUDE_PLUGIN_DATA}")" \
  --stack-ref=main..feat-auth --stack-ref=feat-auth..feat-ui --stack-ref=feat-ui..feat-docs
```

Everything from Step 2 still applies: max bash timeout, no `run_in_background`, `--description` for context. `--stack-ref` cannot be combined with refs, `--staged`, `--untracked`, `--only`, `--all-files`, or `--stdin`; `--include`, `--exclude` and `--annotations` are fine.

To review only part of a stack, pass fewer `--stack-ref` flags. `--include`/`--exclude` filter by **real** path (`app`, `vendor`), not by level label — a label prefix matches nothing and yields an empty tree.

### Reading stack annotations

Every path is prefixed with a synthetic level label, `<ordinal>~<branch>`:

```
## 1~feat-auth/app/main.go:42 (+)
use errors.Is() instead of direct comparison

## 2~feat-ui/app/ui/view.go:10 (-)
don't remove this validation
```

To route an annotation, find the level whose `<ordinal>~<head>` label is a prefix of the path, then strip `label + "/"`; the remainder is the repo-relative path **on that branch**.

**Do not split the path on the first `/`.** Branch names contain slashes (`2~feature/ui-work/app/main.go`), so a positional split attributes the fix to a branch named `2~feature`. Match against the known label set instead — you have it from `detect-stack.sh`.

Classification (Step 3.5) is unchanged; explanation requests are answered the same way. Step 3.6 then decides whether to fix the stack or post each level's comments to its own PR.

### Applying fixes across the stack

Run this only **after** the review session has closed, never while revdiff is open. Work bottom-up: a fix on level 1 moves the base of every level above it.

**Preconditions — check all of them before mutating anything:**

```bash
git rev-parse --abbrev-ref HEAD                       # record START_BRANCH, restore it at the end
git status --porcelain -uno                           # must be empty (untracked files are fine)
ls "$(git rev-parse --git-path rebase-merge)" 2>/dev/null   # must not exist
ls "$(git rev-parse --git-path rebase-apply)" 2>/dev/null   # must not exist
```

**Snapshot every branch tip BEFORE the first commit, and write the table to a temp file.** This is load-bearing twice over: `git rebase --onto` needs each parent's *pre-fix* tip to know which commits belong to the child, and the file is the only way to undo a half-finished restack if the session is lost.

```bash
for b in "${BRANCHES[@]}"; do
  printf '%s %s
' "$b" "$(git rev-parse "$b")"
done > "${TMPDIR:-/tmp}/revdiff-stack-tips-$$.txt"
```

Then, for each level in ascending order:

```bash
git switch "$BRANCH_I"
# levels above the bottom must first be replayed onto their parent's NEW tip
git rebase --onto "$BRANCH_PARENT" "$OLD_TIP_PARENT" "$BRANCH_I"
# apply this level's annotations, then
git commit -am "review: <summary>"
```

`$OLD_TIP_PARENT` is the value from the snapshot, **not** the parent's tip after the parent was rebased — reading it lazily inside the loop is the natural-looking mistake and it silently replays the parent's own commits onto the child. A level with no fixes still needs the rebase, because its parent moved.

Finish with `git switch "$START_BRANCH"`.

**On a rebase conflict, stop.** Never auto-resolve, never `git rebase --skip`. Tell the user plainly: HEAD is detached mid-replay, levels below this one are already rewritten locally, and nothing has been pushed. Offer, in this order:

1. Resolve the conflict, `git rebase --continue`, and resume from the next level.
2. `git rebase --abort` — returns this branch only; lower levels stay rewritten.
3. Full undo — `git rebase --abort`, then `git update-ref refs/heads/<branch> <old tip>` for every level from the snapshot file.

### Pushing

Push last, once, after every level is committed and restacked:

```bash
git push --atomic --force-with-lease --force-if-includes origin feat-auth feat-ui feat-docs
```

`--force-if-includes` is the correct companion to `--force-with-lease`: the lease alone compares against a remote-tracking ref that is stale unless you fetch, and fetching defeats the lease. `--atomic` avoids the window where an already-pushed lower level makes the PR above it show an enormous diff. No `gh pr edit --base` is needed — the tips moved, the topology did not.

**Ask the user before pushing, in its own turn.** Force-pushing rewrites published history and is not reversible from here. Show, per branch, the old remote sha, the new local sha, and the commit-count delta, then wait for an explicit yes. Do not bundle the push into the same tool call as the restack.

## Reviewing a Diff That Lives Outside the Working Tree

Some review targets are not the current repo state: a GitHub PR diff, a patch file on disk, or `git format-patch -1 --stdout` output. Pipe the unified diff into `revdiff --stdin` and the input is parsed as a real multi-file diff (one tree entry per file, hunk navigation, per-file annotations) instead of a context-only buffer. revdiff auto-detects the unified-diff signature; on a malformed patch the input falls back silently to raw-text mode.

Use this instead of the normal launcher flow when:
- the user asks to "review PR #N", "review this patch", "review `gh pr diff` output", or supplies a patch URL/path
- the diff describes commits that are not checked out locally (e.g. someone else's branch on a remote-only PR)
- the user pastes a unified diff and asks for a review of *that diff*, not the working tree

Example invocations (route through the same launcher resolver as the normal flow):

```bash
gh pr diff 123 | "$("${CLAUDE_SKILL_DIR}/scripts/resolve-launcher.sh" launch-revdiff.sh "${CLAUDE_PLUGIN_DATA}")" --stdin
git format-patch -1 --stdout | "$("${CLAUDE_SKILL_DIR}/scripts/resolve-launcher.sh" launch-revdiff.sh "${CLAUDE_PLUGIN_DATA}")" --stdin
cat /tmp/feature.patch | "$("${CLAUDE_SKILL_DIR}/scripts/resolve-launcher.sh" launch-revdiff.sh "${CLAUDE_PLUGIN_DATA}")" --stdin
```

`--stdin` is mutually exclusive with refs, `--staged`, `--only`, `--all-files`, `--include`, `--exclude`, and `--annotations`, so do not combine with the Step 1 ref detection — go directly to Step 3 once the launcher returns. Annotations come back keyed by the real file paths from the diff (not by `--stdin-name`).

## How It Works

1. Launch revdiff in a terminal overlay (agterm full-pane overlay, tmux popup, Zellij floating pane, herdr tab, kitty overlay, wezterm/Kaku split-pane, cmux split, ghostty split+zoom, iTerm2 split pane, or Emacs vterm frame)
2. User navigates the diff, adds annotations on specific lines
3. On quit, annotations are captured from stdout
4. Claude reads annotations and addresses each one
5. Loop: re-launch revdiff to verify fixes, user can add more annotations
6. Done when user quits without annotations

## Workflow

### Step 1: Determine Review Mode

**All-files mode**: If `$ARGUMENTS` matches "all files", "all-files", or "browse all files" (with optional "exclude <prefix>" parts), use **all-files mode**:
- Pass `--all-files` to the launcher
- If user mentions exclude patterns (e.g., "exclude vendor", "exclude vendor and mocks"), pass each as `--exclude=<prefix>`
- Skip ref detection entirely, go directly to Step 2
- Example: "all files exclude vendor" → `--all-files --exclude=vendor`

**File review mode**: If `$ARGUMENTS` is a single token that points at a file on disk (e.g., `docs/plans/feature.md`, `/tmp/notes.txt`, `README.md`, `main.go`, `file.blah`), treat it as file review:
- Decide with `test -f "$ARGUMENTS"` — if the file exists, it's file review mode
- Also treat as file review if the token starts with `/` or `./`, or contains `/` and has a file extension (e.g., `src/app.go`), even when the file is not yet reachable from the current directory
- Skip ref detection entirely
- Go directly to Step 2 with `--only=<filepath>` (no ref argument)
- Works both inside and outside a VCS repo — revdiff reads the file from disk as context-only
- Ambiguous token (e.g., `main` — both a branch name and a potential filename without extension) → prefer ref mode; ask the user only if neither `test -f` nor `git rev-parse --verify` resolves

**Ref mode**: If `$ARGUMENTS` contains explicit ref(s) (e.g., `HEAD~1`, `main`, or `main feature` for two-ref diff), use as-is.

**Auto-detect**: If no ref provided, run the smart detection script:

```bash
${CLAUDE_SKILL_DIR}/scripts/detect-ref.sh
```

The script outputs structured fields:
- `branch`, `main_branch`, `is_main`, `has_uncommitted`, `has_staged_only`
- `suggested_ref` — the ref to pass to revdiff (empty = uncommitted changes)
- `use_staged` — if `true`, pass `--staged` to the launcher (staged-only changes detected)
- `needs_ask` — if `true`, ask the user before proceeding

**When `use_staged: true`**, pass `--staged` to the launcher. This means all changes are in the index (staged) with nothing unstaged — without `--staged`, revdiff would show an empty diff.

**When `needs_ask: true`** (on a feature branch with uncommitted changes), use AskUserQuestion:
- **"Uncommitted only"** — pass no ref (review just working changes)
- **"Branch vs {main_branch}"** — pass main_branch as ref (full branch diff including uncommitted)

**When `needs_ask: false`**, use `suggested_ref` directly:
- On main + uncommitted → no ref (uncommitted changes)
- On main + staged only → no ref + `--staged` (staged changes)
- On main + clean → `HEAD~1` (last commit)
- On feature branch + clean → main branch name (full branch diff)

### Step 2: Launch Review

When you are launching revdiff for the user (e.g., right after a refactor or analysis), pass `--description="..."` so the info popup (`i` key) explains what the change is and what to look at — markdown is supported. For longer prose, write the markdown to a temp file and pass `--description-file=/tmp/revdiff-desc-XXXXXX.md`. The two flags are mutually exclusive; both are optional. Skip when there's no useful context to add.

**When the recent change likely created new untracked files** (new packages, new test files, new docs, new scripts that haven't been `git add`-ed yet), pass `--untracked` so those files appear in the tree. Use this in working-tree mode (no ref, no `--staged`); skip it for ref-to-ref reviews where untracked files are not part of the historical diff.

Pass `--start-at-change` only when the user explicitly asks for that cursor preference; never infer it automatically.

Pass `--filter-unreviewed` only when the user asks for the tree limited to files not marked reviewed; never infer it automatically. The `F` key toggles the same filter during the review.

Run the launcher through the override-chain resolver:

```bash
"$("${CLAUDE_SKILL_DIR}/scripts/resolve-launcher.sh" launch-revdiff.sh "${CLAUDE_PLUGIN_DATA}")" [base] [against] [--staged] [--untracked] [--filter-unreviewed] [--only=file1] [--all-files] [--exclude=prefix] [--description=text|--description-file=path]
```

The resolver and launcher MUST run in the same bash invocation — the resolver runs as a sub-shell substitution so the resolved path is consumed immediately as the executable. The resolver checks `user → bundled` (see `references/install.md` for override paths) and prints the first-found absolute path. Fall-through to the bundled launcher is the default when no overrides exist.

**Failure mode**: if the resolver fails (no launcher in any layer), the command substitution produces an empty string and bash reports `: command not found` with exit 127. The resolver's stderr (`error: launcher not found in override chain: launch-revdiff.sh`) is preserved on the same output stream — check it to confirm the override path is correct (executable bit set, file present in one of the two layers).

**IMPORTANT — long-running command**: The launcher blocks until the user finishes reviewing in the TUI overlay, which can exceed the default bash tool timeout on many harnesses. Set the bash timeout parameter to the **maximum your harness allows** (e.g. 1800000 or higher on OpenCode). The resolver itself returns in milliseconds — the timeout cap applies to the launcher only. Do NOT use `run_in_background` for this — background-task handling is unreliable for interactive TUI launchers (processes may be killed unprompted, and polling loops can leave the session idle after the review finishes). If the review outlasts the timeout cap, the fallback in Step 3 handles it.

**Disconnect-resilient tmux window mode**: when running under tmux, prefix the launcher with `REVDIFF_TMUX_WINDOW=1` to open revdiff in a persistent, server-owned tmux window instead of a client-owned `display-popup`. The review then survives a dropped SSH or tmux client — reattach and it is still there. This is a launcher environment variable, not a revdiff flag.

**Pane-scoped overlay (herdr)**: when running under herdr, `REVDIFF_HERDR_PANE=1` opens revdiff in a zoomed split of the agent's own pane instead of a new fullscreen tab, keeping the agent pane one keypress away. The user sets it in the environment; it falls back to the tab overlay on an older herdr CLI. This is a launcher environment variable, not a revdiff flag.

**Pane-scoped overlay (agterm)**: when running in an agterm split, `REVDIFF_AGTERM_PANE=1` opens revdiff in the agent's own pane instead of over the whole session, leaving the sibling pane live and visible. The user sets it in the environment; it is ignored outside a split. This is a launcher environment variable, not a revdiff flag.

**Claude side panel**: when the `claude` CLI is installed, the launcher switches on revdiff's read-only Claude side panel (`REVDIFF_ASK=1`), so the user can press `V` to select lines and `c` to ask about them without leaving the review. Those questions and answers stay in the panel and never come back as annotations. The user opts out with `REVDIFF_ASK=0`. This is a launcher environment variable, not a flag you pass.

The script:
- Detects available terminal (agterm → tmux → Zellij → herdr → kitty → wezterm/Kaku → cmux → ghostty → iTerm2 → Emacs vterm)
- Launches revdiff in an overlay
- Captures annotation output to a temp file
- Prints captured annotations to stdout

The bundled launcher sets `REVDIFF_EXIT_CODE_ON_ANNOTATIONS`; exit `10` means annotations were captured and is not a launcher failure. Treat other nonzero statuses as failures. On those failures the launcher relays revdiff's own stderr — report that text verbatim instead of guessing which argument was at fault.

### Step 3: Process Annotations

**Collecting launcher output**: In the normal case the launcher returns synchronously with annotations on stdout — process them as described below. If the bash tool reports exit `10`, read stdout and process it as annotations; do not call it a failure. If the bash tool instead reports a timeout (on Claude Code the task keeps running in the background after the 10-minute cap; on other harnesses it may be killed outright), only the launcher process died, but revdiff itself is still open in the overlay and no annotations are lost: revdiff writes them to disk the moment the user quits, and `O` flushes them any time. Do NOT retry the launcher. Use the fallback:

1. Reassure the user and offer both paths, making clear nothing is lost — keep it short and do NOT explain the save mechanics (disk writes, `O` flush, quit-to-save); the user does not need them. Say something like: "The process waiting on your revdiff review timed out and exited — that's harmless, and any annotations you made are safe. Whenever you're done, message me and tell me to either load your annotations and continue, or that you're done and want to stop." Do NOT assume they want to load; quitting with no annotations, or choosing to stop, is a valid outcome.
2. Wait for the user to reply. They cannot respond while the overlay has focus, so their reply means they are back at the session (they quit, or flushed with `O` and switched back).
3. On their reply you MUST act; do not stop at step 1. If they chose to stop, acknowledge and end. Otherwise read the persisted annotations, most recent output file first (the launcher writes to `$TMPDIR` when set, falling back to `/tmp`):
   ```bash
   output_file="$(ls -t "${TMPDIR:-/tmp}"/revdiff-output-* 2>/dev/null | head -1)"
   if [ -n "$output_file" ] && [ -f "$output_file" ]; then
     cat "$output_file"
   fi
   ```
4. If the output file has content, process it as annotations below. If it is empty or missing, fall back to the durable review history, which survives even when the launcher's cleanup removed the temp file: run `"${CLAUDE_SKILL_DIR}/scripts/read-latest-history.sh"` and process the annotations from its `## Annotations` section (see "Using Existing Review History"). Only if both are empty did the user quit without annotating.

Both reads return complete content: revdiff writes the output file atomically on exit, and the history entry is complete before the process exits. That guarantees no partial read, not that the file belongs to this review: with two reviews live under one `$TMPDIR`, the newest match may belong to the other one.

A reviewer may also keep revdiff open on purpose and press `O` to flush the current annotations to the same output file mid-session, without quitting. The flush uses the same atomic write, so the fallback read above still returns a complete file. When the user says something like "I flushed my notes, go ahead" while the overlay is still open, read the most recent output file exactly as in the timeout fallback and process the annotations; do NOT relaunch revdiff. After you finish the code changes, the reviewer reloads with `R` and continues in the same session. No launcher flags change for this — the launcher already passes an output file, and `O` reuses it.

If the script produces output, the user made annotations. The output format is:

```
## file.go:43 (+)
use errors.Is() instead of direct comparison

## store.go:18 (-)
don't remove this validation
```

Each annotation block has:
- `## filename:line (type)` — which file and line, `(+)` = added, `(-)` = removed, `(file-level)` = file note
- Comment text below — what the user wants changed

### Step 3.5: Classify Annotations

Split annotations into two categories:

**Explanation requests** — annotation matches either rule (case-insensitive):
- contains two or more consecutive question marks anywhere in the text (`??`, `???`, etc.) — a language-neutral shortcut for "please explain"
- OR starts with one of: `explain`, `remind`, `describe`, `what is`, `what are`, `how does`, `how do`, `clarify`

These are questions the user wants answered, not code changes.

**Code-change directives** — everything else. These are instructions to modify code.

**If explanation requests are found:**

1. Answer each explanation request — read the referenced code, generate a clear markdown explanation
2. If there are also code-change directives in the same batch, note them as pending (they carry over to Step 4 after the explanation loop)
3. Enter the **explanation loop**:

   a. Write the explanation to a temp markdown file (e.g., `/tmp/revdiff-explain-XXXXXX.md`)
   b. Launch revdiff with `--only=/tmp/revdiff-explain-XXXXXX.md` via the launcher script — this opens the explanation as a scrollable markdown view with TOC sidebar
   c. **If user quits without annotations** → explanation accepted, clean up temp file, proceed:
      - If pending code-change directives exist → go to Step 3.6
      - Otherwise → go to Step 6 (re-launch revdiff with the original diff ref)
   d. **If user annotates the explanation** → these are follow-up questions or clarification requests. Read the annotations, refine/extend the explanation markdown, write updated temp file, go back to step (b)

The explanation loop continues until the user quits without annotating. This allows a natural back-and-forth dialogue where the user can ask for more detail or corrections on specific parts of the explanation.

**If no explanation requests** — all annotations are code-change directives, proceed directly to Step 3.6.

### Step 3.6: Fix or Post to GitHub

Before planning fixes, decide whether the code-change annotations are for you to fix, or review comments for a PR someone else owns.

The review is **of a PR** when it was a stack review (each level's PR number is in the `detect-stack.sh` table), a `gh pr diff <N> | ... --stdin` review (PR N), or a ref review whose current branch has an open PR (`gh pr view --json number,author,url` succeeds). Working-tree, `--only`, `--all-files` and other non-PR reviews skip this step: go to Step 4.

Recommend by authorship: compare each PR's `author.login` with `gh api user --jq .login`. All yours → recommend **Fix them**; none yours → recommend **Post as a draft GitHub review**; mixed → no recommendation. Ask with AskUserQuestion: **Fix them** or **Post as a draft GitHub review**.

**Post path:**

1. Write the annotations exactly as received to a temp file (e.g. `/tmp/revdiff-post-XXXXXX.md`).
2. Dry run per PR:
   ```bash
   "${CLAUDE_SKILL_DIR}/scripts/post-gh-review.py" --pr <N> --annotations <file> --dry-run
   ```
   Stack review: run once per level with `--prefix "<label>/"` and that level's PR, so each PR gets only its own annotations with the label stripped. A PR in another repository: add `--repo OWNER/REPO`.
3. Show the user each PR's summary and preview lines verbatim (`RIGHT`/`LEFT` = inline comment, `BODY` = moved into the review body because the line is outside the PR diff or the note is file-level, `SKIP` = explanation request, never posted). Ask for an explicit yes before posting, in its own turn.
4. Run the same commands without `--dry-run`. Report each `review_url:` and say plainly that the review is a **draft**: only the user sees it until they press Submit on GitHub. On `error:`, report it verbatim.
5. Stop. Do not fix code and do not re-launch revdiff.

**Fix path:** go to Step 4.

### Step 4: Plan Changes

Enter plan mode (EnterPlanMode) to analyze code-change annotations:
- List each annotation with file and line reference
- Describe the planned change for each
- Get user approval before modifying code

### Step 5: Address Annotations

After plan approval, fix the actual source code. Each annotation is a directive.

### Step 6: Loop

After fixing (or after "Continue review" from Step 3.5), run the launcher script again with the same ref. The user can:
- Add more annotations → go back to Step 3
- Quit without annotations → review complete (no output)

### Step 7: Done

When the script produces no output, the review is complete. Inform the user.

## Example Sessions

```
User: "revdiff HEAD~1"
→ launch revdiff in tmux popup with HEAD~1 diff
→ user annotates: "handler.go:43 - use errors.Is()"
→ user quits
→ annotations captured
→ enter plan mode: "add errors.Is() check at handler.go:43"
→ user approves
→ fix applied
→ re-launch revdiff HEAD~1
→ user sees fix, quits without annotations
→ "review complete"
```

```
User: "revdiff HEAD~3"
→ launch revdiff in tmux popup with HEAD~3 diff
→ user annotates: "server.go:72 - explain what this mutex protects"
→ user quits
→ annotation classified as explanation request (starts with "explain")
→ Claude reads server.go:72, generates markdown explanation
→ writes to /tmp/revdiff-explain-XXXXXX.md
→ launch revdiff --only=/tmp/revdiff-explain-XXXXXX.md (explanation view with TOC)
→ user reads explanation, annotates: "what about the race condition on line 80?"
→ Claude refines explanation, rewrites temp file
→ re-launch revdiff --only=/tmp/revdiff-explain-XXXXXX.md
→ user reads updated explanation, quits without annotations
→ explanation accepted, clean up temp file
→ re-launch revdiff HEAD~3 (back to diff review)
→ user quits without annotations
→ "review complete"
```

```
User: "revdiff all files exclude vendor"
→ launch revdiff with --all-files --exclude=vendor
→ user browses all tracked files, annotates as needed
→ same annotation loop as above
```

```
User: "revdiff docs/plans/feature.md"
→ test -f docs/plans/feature.md succeeds → file review mode
→ launch revdiff with --only=docs/plans/feature.md (context-only view, no ref)
→ user annotates prose: "section 'Open questions':3 - drop this, resolved"
→ user quits
→ same annotation loop as above (applies to the file content)
```
