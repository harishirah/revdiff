#!/usr/bin/env python3
"""post-gh-review.py - post revdiff annotations to a GitHub pull request as a draft review.

usage: post-gh-review.py --pr N --annotations FILE [--repo OWNER/REPO] [--prefix LABEL/] [--dry-run]

each annotation becomes a review comment: (+) and context lines comment on the
new side (RIGHT), (-) lines on the old side (LEFT), and N-M ranges become
multi-line comments. annotations GitHub would reject - lines outside the PR
diff, ranges spanning hunks - and file-level notes go into the review body
instead, so one bad line never fails the whole review. explanation requests
("??", or starting with explain/what is/...) are questions for the agent and
are never posted.

the review is created as a pending draft: only its author sees it until they
submit it on GitHub. --prefix keeps only annotations whose path starts with the
given stack label and strips it, for posting one level of a --stack-ref review.
--dry-run prints the summary and preview without calling the API.

output: flat "key: value" lines, then one preview line per annotation:
  <SIDE> <path:lines>  <first line of the comment>
where SIDE is RIGHT/LEFT (inline), BODY (moved to the review body) or SKIP
(explanation request). errors go to stderr with exit status 1.
"""

import argparse
import json
import re
import subprocess
import sys

HEADER_RE = re.compile(r"^## (.+?)(?::(\d+)(?:-(\d+))?)? \((file-level|\+|-| )\)$")
HUNK_RE = re.compile(r"^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@")
QUESTION_PREFIXES = ("explain", "remind", "describe", "what is", "what are", "how does", "how do", "clarify")


class GhError(Exception):
    pass


def gh(args, stdin=None):
    proc = subprocess.run(["gh"] + args, input=stdin, capture_output=True, text=True)
    if proc.returncode != 0:
        raise GhError((proc.stderr or proc.stdout).strip() or "gh %s failed" % " ".join(args))
    return proc.stdout


def parse_annotations(text):
    """parse revdiff's annotation output into dicts: file, line, end, type, body."""
    out = []
    cur = None
    body = []

    def flush():
        if cur is None:
            return
        lines = list(body)
        if lines and lines[-1] == "":
            lines.pop()
        cur["body"] = "\n".join(lines)
        out.append(cur)

    for line in text.splitlines():
        m = HEADER_RE.match(line) if line.startswith("## ") else None
        if m:
            flush()
            cur = {
                "file": m.group(1),
                "line": int(m.group(2)) if m.group(2) else 0,
                "end": int(m.group(3)) if m.group(3) else 0,
                "type": m.group(4),
            }
            body = []
            continue
        if cur is None:
            if line.strip():
                raise ValueError("unexpected text before the first annotation: %r" % line)
            continue
        # undo the leading-space escape revdiff adds to body lines starting with "## "
        if line.startswith(" ") and line.lstrip(" ").startswith("## "):
            line = line[1:]
        body.append(line)
    flush()
    return out


def is_question(body):
    text = body.strip().lower()
    return "??" in text or text.startswith(QUESTION_PREFIXES)


def parse_diff(text):
    """map each file in a unified diff to the lines GitHub accepts comments on.

    returns {path: {"RIGHT": {line: hunk}, "LEFT": {line: hunk}}}; hunk is an
    index used to keep multi-line comments inside a single hunk.
    """
    files = {}
    old_path = new_path = None
    sides = None
    old_rem = new_rem = 0
    old_no = new_no = 0
    hunk = 0

    for line in text.splitlines():
        if old_rem > 0 or new_rem > 0:
            tag = line[:1]
            if tag == "+":
                sides["RIGHT"][new_no] = hunk
                new_no += 1
                new_rem -= 1
            elif tag == "-":
                sides["LEFT"][old_no] = hunk
                old_no += 1
                old_rem -= 1
            elif tag == " " or line == "":
                sides["RIGHT"][new_no] = hunk
                sides["LEFT"][old_no] = hunk
                new_no += 1
                old_no += 1
                new_rem -= 1
                old_rem -= 1
            continue  # "\ No newline at end of file" consumes nothing
        if line.startswith("diff --git "):
            old_path = new_path = None
            sides = None
        elif line.startswith("--- "):
            old_path = strip_diff_path(line[4:])
        elif line.startswith("+++ "):
            new_path = strip_diff_path(line[4:])
            path = new_path if new_path is not None else old_path
            if path is not None:
                sides = files.setdefault(path, {"RIGHT": {}, "LEFT": {}})
        else:
            m = HUNK_RE.match(line)
            if m and sides is not None:
                hunk += 1
                old_no, new_no = int(m.group(1)), int(m.group(3))
                old_rem = int(m.group(2)) if m.group(2) is not None else 1
                new_rem = int(m.group(4)) if m.group(4) is not None else 1
    return files


def strip_diff_path(raw):
    raw = raw.rstrip("\t")
    if raw == "/dev/null":
        return None
    if raw.startswith(("a/", "b/")):
        return raw[2:]
    return raw


def label(a):
    if a["line"] == 0:
        return "%s (file-level)" % a["file"]
    if a["end"]:
        return "%s:%d-%d" % (a["file"], a["line"], a["end"])
    return "%s:%d" % (a["file"], a["line"])


def build_review(annotations, diff_lines, head_sha):
    """split annotations into inline comments, body notes and skipped questions."""
    comments, notes, preview = [], [], []
    skipped = 0
    for a in annotations:
        first = a["body"].strip().splitlines()[0] if a["body"].strip() else ""
        if is_question(a["body"]):
            skipped += 1
            preview.append("SKIP  %s  %s" % (label(a), first))
            continue
        if a["line"] == 0:
            notes.append(a)
            preview.append("BODY  %s  %s" % (label(a), first))
            continue

        side = "LEFT" if a["type"] == "-" else "RIGHT"
        valid = diff_lines.get(a["file"], {}).get(side, {})
        start, end = a["line"], a["end"] or a["line"]
        comment = {"path": a["file"], "side": side, "body": a["body"]}
        if start != end and start in valid and end in valid and valid[start] == valid[end]:
            comment.update(start_line=start, start_side=side, line=end)
        elif start == end and start in valid:
            comment["line"] = start
        else:
            notes.append(a)
            preview.append("BODY  %s  (outside the PR diff) %s" % (label(a), first))
            continue
        comments.append(comment)
        preview.append("%-5s %s  %s" % (side, label(a), first))

    body = "\n\n".join("**`%s`**\n%s" % (label(a), a["body"]) for a in notes)
    payload = {"commit_id": head_sha, "body": body, "comments": comments}
    return payload, len(notes), skipped, preview


def main(argv=None):
    ap = argparse.ArgumentParser(description="post revdiff annotations to a PR as a draft review")
    ap.add_argument("--pr", required=True, type=int, help="pull request number")
    ap.add_argument("--annotations", required=True, help="file with revdiff annotation output")
    ap.add_argument("--repo", help="OWNER/REPO, when the PR is not in the current repository")
    ap.add_argument("--prefix", default="", help="keep only paths with this stack label prefix, and strip it")
    ap.add_argument("--dry-run", action="store_true", help="print the summary and preview, post nothing")
    args = ap.parse_args(argv)

    repo_args = ["--repo", args.repo] if args.repo else []
    try:
        with open(args.annotations, encoding="utf-8") as f:
            annotations = parse_annotations(f.read())
        if args.prefix:
            annotations = [dict(a, file=a["file"][len(args.prefix):]) for a in annotations
                           if a["file"].startswith(args.prefix)]

        pr = json.loads(gh(["pr", "view", str(args.pr), "--json", "headRefOid,url"] + repo_args))
        m = re.match(r"https://[^/]+/([^/]+/[^/]+)/pull/\d+", pr["url"])
        if not m:
            raise GhError("cannot read the repository from PR url %s" % pr["url"])
        diff_lines = parse_diff(gh(["pr", "diff", str(args.pr)] + repo_args))
        payload, in_body, skipped, preview = build_review(annotations, diff_lines, pr["headRefOid"])
    except (OSError, ValueError, GhError) as e:
        print("error: %s" % e, file=sys.stderr)
        return 1

    print("pr: %d" % args.pr)
    print("pr_url: %s" % pr["url"])
    print("inline_comments: %d" % len(payload["comments"]))
    print("body_notes: %d" % in_body)
    print("skipped_questions: %d" % skipped)

    if not payload["comments"] and not payload["body"]:
        print("review_url: ")
        print("posted: false (nothing to post)")
        print("\n".join(preview))
        return 0
    if args.dry_run:
        print("review_url: ")
        print("posted: false (dry run)")
        print("\n".join(preview))
        return 0

    endpoint = "repos/%s/pulls/%d/reviews" % (m.group(1), args.pr)
    try:
        resp = json.loads(gh(["api", "-X", "POST", endpoint, "--input", "-"], stdin=json.dumps(payload)))
    except GhError as e:
        msg = str(e)
        if "pending review" in msg.lower():
            msg += " (you already have a draft review on this PR: submit or delete it on GitHub, then re-run)"
        print("error: %s" % msg, file=sys.stderr)
        return 1
    print("review_url: %s" % resp.get("html_url", pr["url"]))
    print("posted: true (draft - submit it on GitHub to publish)")
    print("\n".join(preview))
    return 0


if __name__ == "__main__":
    sys.exit(main())
