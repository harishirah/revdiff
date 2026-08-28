#!/usr/bin/env bash
# detect-stack.sh - GitHub PR stack detection for the revdiff skill.
# source: .claude-plugin/skills/revdiff/scripts/detect-stack.sh (keep in sync)
# discovers the chain of stacked pull requests containing the current branch by
# following each PR's base branch, and emits the ref ranges revdiff needs.
#
# git only: a stack is a chain of branches, and the PR metadata comes from the
# gh CLI. requires gh to be installed and authenticated.
#
# output fields (always all of them; empty value when not applicable):
#   vcs: git, or empty when the repo is not git
#   stack_ok: true/false (whether a usable stack was found)
#   current_branch: branch checked out now
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

if ! command -v gh >/dev/null 2>&1; then
    fail "gh CLI not found (install: https://cli.github.com)"
fi
if ! gh auth status >/dev/null 2>&1; then
    fail "gh not authenticated (run: gh auth login)"
fi

# one API call; gh bundles gojq so no jq dependency. tab-separated because a PR
# title may contain anything except a tab or newline after sanitizing.
pr_data=""
if ! pr_data=$(gh pr list --state open --limit 100 \
    --json number,title,headRefName,baseRefName,url \
    --jq '.[] | [.headRefName, .baseRefName, .number, .url, .title] | @tsv' 2>/dev/null); then
    fail "gh pr list failed (not a GitHub remote, or no access)"
fi
if [ -z "$pr_data" ]; then
    fail "no open pull requests found"
fi

# index the PR list by head branch and count how many PRs target each base, so
# a fork (two PRs sharing a base) can be reported rather than silently picked
declare -a all_heads=() all_bases=() all_prs=() all_urls=() all_titles=()
while IFS=$'\t' read -r h b n u t; do
    [ -z "$h" ] && continue
    all_heads+=("$h")
    all_bases+=("$b")
    all_prs+=("$n")
    all_urls+=("$u")
    all_titles+=("$(sanitize "$t")")
done <<<"$pr_data"

# index_of_head <branch> -> echoes index, or empty
index_of_head() {
    local want="$1" i
    for ((i = 0; i < ${#all_heads[@]}; i++)); do
        if [ "${all_heads[$i]}" = "$want" ]; then
            echo "$i"
            return 0
        fi
    done
    return 1
}

# children_of <branch> -> echoes space-separated heads of PRs targeting it
children_of() {
    local want="$1" i out=""
    for ((i = 0; i < ${#all_heads[@]}; i++)); do
        if [ "${all_bases[$i]}" = "$want" ]; then
            out="$out ${all_heads[$i]}"
        fi
    done
    echo "${out# }"
}

if ! index_of_head "$current_branch" >/dev/null 2>&1 && [ -z "$(children_of "$current_branch")" ]; then
    fail "no open PR found for branch $current_branch"
fi

# walk DOWN from the current branch to the trunk, collecting ancestors.
# visited guards against a base cycle, which gh will happily report.
declare -a chain=()
declare -a visited=()
seen() {
    local want="$1" v
    for v in ${visited[@]+"${visited[@]}"}; do
        [ "$v" = "$want" ] && return 0
    done
    return 1
}

cur="$current_branch"
while idx=$(index_of_head "$cur" 2>/dev/null); do
    if seen "$cur"; then
        fail "PR base cycle detected at $cur"
    fi
    visited+=("$cur")
    chain=("$idx" ${chain[@]+"${chain[@]}"}) # prepend: bottom-most ends up first
    cur="${all_bases[$idx]}"
done
trunk="$cur"

# walk UP from the current branch through descendants; stop at a fork so the
# skill can ask which line of the stack to review
top="$current_branch"
while true; do
    kids=$(children_of "$top")
    [ -z "$kids" ] && break
    # shellcheck disable=SC2206  # deliberate word splitting on the space-joined list
    kid_arr=($kids)
    if [ "${#kid_arr[@]}" -gt 1 ]; then
        fork_at="$top"
        fork_candidates=$(printf '%s,' "${kid_arr[@]}")
        fork_candidates="${fork_candidates%,}"
        needs_ask="true"
        break
    fi
    if seen "${kid_arr[0]}"; then
        fail "PR base cycle detected at ${kid_arr[0]}"
    fi
    visited+=("${kid_arr[0]}")
    kidx=$(index_of_head "${kid_arr[0]}")
    chain+=("$kidx")
    top="${kid_arr[0]}"
done

for idx in ${chain[@]+"${chain[@]}"}; do
    heads+=("${all_heads[$idx]}")
    bases+=("${all_bases[$idx]}")
    prs+=("${all_prs[$idx]}")
    urls+=("${all_urls[$idx]}")
    titles+=("${all_titles[$idx]}")
done

stack_len="${#heads[@]}"
if [ "$stack_len" -eq 0 ]; then
    fail "no open PR found for branch $current_branch"
fi

for ((i = 0; i < ${#heads[@]}; i++)); do
    if [ "${heads[$i]}" = "$current_branch" ]; then
        current_level=$((i + 1))
    fi
done

# every head and the trunk must exist locally: revdiff diffs local branch
# ranges, and the agent later checks them out to apply fixes
missing=""
for h in ${heads[@]+"${heads[@]}"}; do
    git show-ref --verify --quiet "refs/heads/$h" 2>/dev/null || missing="$h"
done
if ! git show-ref --verify --quiet "refs/heads/$trunk" 2>/dev/null; then
    fail "base branch $trunk not present locally (run: git fetch origin $trunk:$trunk)"
fi
if [ -n "$missing" ]; then
    fail "branch $missing not present locally (run: git fetch && git switch $missing)"
fi

stack_ok="true"
emit
