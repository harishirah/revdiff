#!/usr/bin/env bash
# detect-stack.sh - GitHub PR stack detection for the revdiff skill.
# source: .claude-plugin/skills/revdiff/scripts/detect-stack.sh (keep in sync)
# discovers the chain of stacked pull requests containing a branch by following
# each PR's base branch, and emits the ref ranges revdiff needs.
#
# usage: detect-stack.sh [branch]
#   branch: start from this branch instead of the checked-out one. used to
#           re-scope after a fork: pass the fork candidate the user picked.
#
# PRs are looked up one branch at a time with gh's server-side --head/--base
# filters, so detection does not depend on how many open PRs the repository
# has. listing open PRs and filtering locally misses the stack as soon as the
# repository has more open PRs than the list limit.
#
# git only: a stack is a chain of branches, and the PR metadata comes from the
# gh CLI. requires gh to be installed and authenticated.
#
# output fields (always all of them; empty value when not applicable):
#   vcs: git, or empty when the repo is not git
#   stack_ok: true/false (whether a usable stack was found)
#   current_branch: branch checked out now
#   start_branch: branch the walk started from (the argument, else current_branch)
#   trunk: the branch the bottom PR targets (e.g. main)
#   stack_len: number of levels
#   current_level: 1-based position of current_branch in the stack, empty if absent
#   level_N_head:  branch name of level N (N is 1-based, bottom-up)
#   level_N_base:  branch that level N targets
#   level_N_ref:   "base..head" — pass verbatim as --stack-ref=<ref>, single token
#   level_N_pr:    pull request number
#   level_N_url:   pull request url
#   level_N_title: pull request title (sanitized, truncated)
#   level_N_local: true/false (head branch exists locally)
#   fork_at: branch where two open PRs share a base, empty when linear
#   fork_candidates: comma-separated heads of the competing PRs
#   needs_ask: true/false (whether the skill should ask the user)
#   error: human-actionable message, empty on success

set -euo pipefail

# NOTE: this script must run under bash 3.2 (stock /bin/bash on macOS, which
# `#!/usr/bin/env bash` resolves to when no newer bash is on PATH). There,
# "${arr[@]}" on an EMPTY array is an unbound-variable error under `set -u`,
# so every possibly-empty array expansion below uses ${arr[@]+"${arr[@]}"}.

vcs=""
stack_ok="false"
current_branch=""
start_branch=""
trunk=""
stack_len="0"
current_level=""
fork_at=""
fork_candidates=""
needs_ask="false"
error=""

# parallel arrays, index 0 = bottom of stack
heads=()
bases=()
prs=()
urls=()
titles=()

emit() {
    echo "vcs: $vcs"
    echo "stack_ok: $stack_ok"
    echo "current_branch: $current_branch"
    echo "start_branch: $start_branch"
    echo "trunk: $trunk"
    echo "stack_len: $stack_len"
    local i n
    n=${#heads[@]}
    for ((i = 0; i < n; i++)); do
        local head="${heads[$i]}" local_flag="false"
        if git show-ref --verify --quiet "refs/heads/$head" 2>/dev/null; then
            local_flag="true"
        fi
        echo "level_$((i + 1))_head: $head"
        echo "level_$((i + 1))_base: ${bases[$i]}"
        echo "level_$((i + 1))_ref: ${bases[$i]}..$head"
        echo "level_$((i + 1))_pr: ${prs[$i]}"
        echo "level_$((i + 1))_url: ${urls[$i]}"
        echo "level_$((i + 1))_title: ${titles[$i]}"
        echo "level_$((i + 1))_local: $local_flag"
    done
    echo "current_level: $current_level"
    echo "fork_at: $fork_at"
    echo "fork_candidates: $fork_candidates"
    echo "needs_ask: $needs_ask"
    echo "error: $error"
}

fail() {
    error="$1"
    stack_ok="false"
    needs_ask="true"
    emit
    exit 0
}

# strip control characters and truncate, so a crafted PR title cannot break the
# one-field-per-line output contract or inject terminal escapes
sanitize() {
    printf '%s' "$1" | tr -d '\000-\037\177' | cut -c1-80
}

if ! command -v git >/dev/null 2>&1 || ! git rev-parse --git-dir >/dev/null 2>&1; then
    fail "not a git repository (stack review is git only)"
fi
vcs="git"
current_branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "")
start_branch="${1:-$current_branch}"

if ! command -v gh >/dev/null 2>&1; then
    fail "gh CLI not found (install: https://cli.github.com)"
fi
if ! gh auth status >/dev/null 2>&1; then
    fail "gh not authenticated (run: gh auth login)"
fi
if ! git show-ref --verify --quiet "refs/heads/$start_branch" 2>/dev/null; then
    fail "branch $start_branch not present locally (run: git fetch origin $start_branch:$start_branch)"
fi

# PRs from forks are skipped: a stack level must be a branch of this repository,
# and a fork's PR can share a head name with a local branch it has nothing to do
# with. gh bundles gojq, so --jq needs no jq install. tab-separated because a PR
# title may contain anything except a tab or newline after sanitizing.

# pr_for_head <branch>: sets pr_base, pr_number, pr_url, pr_title from the open
# PR whose head is <branch>; returns 1 when there is none. must not run in a
# subshell: fail has to exit the script, not the subshell.
pr_for_head() {
    local out
    if ! out=$(gh pr list --state open --head "$1" --limit 10 \
        --json number,title,baseRefName,url,isCrossRepository \
        --jq '[.[] | select(.isCrossRepository | not)] | first // empty | [.baseRefName, .number, .url, .title] | @tsv' 2>/dev/null); then
        fail "gh pr list failed (not a GitHub remote, or no access)"
    fi
    [ -z "$out" ] && return 1
    IFS=$'\t' read -r pr_base pr_number pr_url pr_title <<<"$out"
    pr_title=$(sanitize "$pr_title")
}

# prs_for_base <branch>: fills the kid_* arrays from the open PRs targeting
# <branch>. same subshell caveat as pr_for_head.
prs_for_base() {
    local out h n u t
    kid_heads=()
    kid_prs=()
    kid_urls=()
    kid_titles=()
    if ! out=$(gh pr list --state open --base "$1" --limit 20 \
        --json number,title,headRefName,url,isCrossRepository \
        --jq '.[] | select(.isCrossRepository | not) | [.headRefName, .number, .url, .title] | @tsv' 2>/dev/null); then
        fail "gh pr list failed (not a GitHub remote, or no access)"
    fi
    while IFS=$'\t' read -r h n u t; do
        [ -z "$h" ] && continue
        kid_heads+=("$h")
        kid_prs+=("$n")
        kid_urls+=("$u")
        kid_titles+=("$(sanitize "$t")")
    done <<<"$out"
}

# visited guards against a base cycle, which gh will happily report
visited=()
seen() {
    local want="$1" v
    for v in ${visited[@]+"${visited[@]}"}; do
        [ "$v" = "$want" ] && return 0
    done
    return 1
}

# walk DOWN from the start branch to the trunk: each PR's base is the level
# below it, and the first base with no open PR of its own is the trunk
cur="$start_branch"
while pr_for_head "$cur"; do
    if seen "$cur"; then
        fail "PR base cycle detected at $cur"
    fi
    visited+=("$cur")
    # prepend: bottom-most ends up first
    heads=("$cur" ${heads[@]+"${heads[@]}"})
    bases=("$pr_base" ${bases[@]+"${bases[@]}"})
    prs=("$pr_number" ${prs[@]+"${prs[@]}"})
    urls=("$pr_url" ${urls[@]+"${urls[@]}"})
    titles=("$pr_title" ${titles[@]+"${titles[@]}"})
    cur="$pr_base"
done
trunk="$cur"

if [ "${#heads[@]}" -eq 0 ]; then
    trunk=""
    fail "no open PR found for branch $start_branch"
fi

# walk UP from the start branch through descendants; stop at a fork so the
# skill can ask which line of the stack to review
top="$start_branch"
while true; do
    prs_for_base "$top"
    [ "${#kid_heads[@]}" -eq 0 ] && break
    if [ "${#kid_heads[@]}" -gt 1 ]; then
        fork_at="$top"
        fork_candidates=$(printf '%s,' "${kid_heads[@]}")
        fork_candidates="${fork_candidates%,}"
        needs_ask="true"
        break
    fi
    if seen "${kid_heads[0]}"; then
        fail "PR base cycle detected at ${kid_heads[0]}"
    fi
    visited+=("${kid_heads[0]}")
    heads+=("${kid_heads[0]}")
    bases+=("$top")
    prs+=("${kid_prs[0]}")
    urls+=("${kid_urls[0]}")
    titles+=("${kid_titles[0]}")
    top="${kid_heads[0]}"
done

stack_len="${#heads[@]}"

for ((i = 0; i < ${#heads[@]}; i++)); do
    if [ "${heads[$i]}" = "$current_branch" ]; then
        current_level=$((i + 1))
    fi
done

# every head and the trunk must exist locally: revdiff diffs local branch
# ranges, and the agent later checks them out to apply fixes. all missing heads
# are reported at once, so one fetch fixes a stack that was never checked out.
if ! git show-ref --verify --quiet "refs/heads/$trunk" 2>/dev/null; then
    fail "base branch $trunk not present locally (run: git fetch origin $trunk:$trunk)"
fi
missing=""
refspecs=""
for h in ${heads[@]+"${heads[@]}"}; do
    if ! git show-ref --verify --quiet "refs/heads/$h" 2>/dev/null; then
        missing="${missing:+$missing, }$h"
        refspecs="$refspecs $h:$h"
    fi
done
if [ -n "$missing" ]; then
    fail "branches not present locally: $missing (run: git fetch origin$refspecs)"
fi

stack_ok="true"
emit
