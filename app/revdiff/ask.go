package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/umputun/revdiff/app/ask"
)

// setupAsker creates the Claude conversation behind the side panel, or nil
// when --ask is off.
func setupAsker(opts options, workDir, description string) (*ask.Claude, error) {
	if !opts.Ask {
		return nil, nil
	}
	bin, err := exec.LookPath(opts.AskBin)
	if err != nil {
		return nil, fmt.Errorf("--ask: claude CLI not found: %w", err)
	}
	dir := workDir
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return nil, fmt.Errorf("--ask: %w", err)
		}
	}
	c, err := ask.New(ask.Options{Bin: bin, WorkDir: dir, Model: opts.AskModel, Context: askReviewContext(opts, workDir, description)})
	if err != nil {
		return nil, fmt.Errorf("--ask: %w", err)
	}
	return c, nil
}

// askReviewContext describes the review for the side panel's system prompt.
func askReviewContext(opts options, workDir, description string) string {
	var b strings.Builder
	if workDir != "" {
		fmt.Fprintf(&b, "Repository: %s\n", workDir)
	}
	switch {
	case opts.stackMode():
		b.WriteString("Reviewing a stack of pull requests, bottom first:\n")
		for i, lv := range opts.stackLevels {
			fmt.Fprintf(&b, "- PR %d: branch %s, diff %s..%s\n", i+1, lv.head, lv.base, lv.head)
		}
	case opts.Stdin:
		b.WriteString("Reviewing a diff or text piped on stdin; it may not match the checked-out files.\n")
	case opts.compareAbsNew != "":
		fmt.Fprintf(&b, "Comparing two files: %s (old) and %s (new).\n", opts.compareAbsOld, opts.compareAbsNew)
	case opts.AllFiles:
		b.WriteString("Browsing all tracked files, not a diff.\n")
	case len(opts.Only) > 0:
		fmt.Fprintf(&b, "Reviewing these files: %s\n", strings.Join(opts.Only, ", "))
	case opts.Staged:
		b.WriteString("Reviewing staged changes (git diff --cached).\n")
	case opts.ref() != "":
		fmt.Fprintf(&b, "Reviewing the diff for %s.\n", opts.ref())
	default:
		b.WriteString("Reviewing uncommitted changes in the working tree.\n")
	}
	if description != "" {
		b.WriteString("\nWhat the change is about, from whoever opened the review:\n" + description + "\n")
	}
	return b.String()
}
