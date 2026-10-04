// Package ask answers questions about a review by running the claude CLI
// headless. One Claude value is one conversation: the first question starts a
// session with a fixed id and later questions resume it, so follow-ups see the
// earlier answers.
package ask

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// readOnlyTools are what the session may use without asking: reading the
// repository and read-only git commands. Plan mode plus the denied list keeps
// it read-only even when the user's own settings allow edits.
var (
	readOnlyTools = []string{"Read", "Grep", "Glob", "Bash(git show:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git blame:*)"}
	deniedTools   = []string{"Edit", "Write", "NotebookEdit", "ExitPlanMode"}
)

const systemPrompt = `You are answering questions from a developer who is reading a diff in revdiff, a terminal code-review tool. They want to understand the change, not change it.
- Lead with the answer and keep it short.
- The answer is shown in a narrow terminal side panel: use short paragraphs and lists, no tables.
- You are read-only. Read files and use git to look things up; never offer to edit anything.
- A file that exists only on another branch can be read with: git show <branch>:<path>`

// Options configures a Claude conversation.
type Options struct {
	Bin     string // claude executable; "claude" when empty
	WorkDir string // directory the session runs in, normally the repository root
	Model   string // model alias or id; the CLI default when empty
	Context string // description of the review, appended to the system prompt
}

// Claude is one conversation with the claude CLI.
type Claude struct {
	opts      Options
	sessionID string

	mu      sync.Mutex // serializes questions; the session takes one turn at a time
	started bool       // the session exists, so later questions resume it

	cancelMu sync.Mutex // guards cancel, which Close reads from another goroutine
	cancel   context.CancelFunc
}

// New creates a conversation. Nothing runs until the first Ask.
func New(opts Options) (*Claude, error) {
	if opts.Bin == "" {
		opts.Bin = "claude"
	}
	id, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("ask: session id: %w", err)
	}
	return &Claude{opts: opts, sessionID: id}, nil
}

// SessionID returns the id of the conversation, usable with `claude --resume`.
func (c *Claude) SessionID() string { return c.sessionID }

// Ask sends one question and streams the answer text to onChunk as it arrives.
// It blocks until the answer is complete, ctx is canceled, or Close is called.
func (c *Claude) Ask(ctx context.Context, prompt string, onChunk func(string)) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	c.setCancel(cancel)
	defer func() {
		cancel()
		c.setCancel(nil)
	}()

	cmd := exec.CommandContext(ctx, c.opts.Bin, c.args()...) //nolint:gosec // binary and args come from revdiff's own config
	cmd.Dir = c.opts.WorkDir
	cmd.Stdin = strings.NewReader(prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ask: %w", err)
	}
	cmd.WaitDelay = time.Second // the same holds for the stderr copy Wait performs
	cmd.Cancel = func() error {
		// close the pipe too: a child of claude can hold it open after the kill,
		// which would leave the reader below blocked
		_ = stdout.Close()
		return cmd.Process.Kill()
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ask: start %s: %w", c.opts.Bin, err)
	}

	res := c.readStream(stdout, onChunk)
	waitErr := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case res.err != nil:
		return res.err
	case !res.done && waitErr != nil:
		return fmt.Errorf("ask: claude failed: %w: %s", waitErr, lastLine(stderr.String()))
	case !res.done:
		return errors.New("ask: claude ended without an answer")
	}
	return nil
}

// Close stops a question in flight, if any.
func (c *Claude) Close() {
	c.cancelMu.Lock()
	defer c.cancelMu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *Claude) setCancel(cancel context.CancelFunc) {
	c.cancelMu.Lock()
	c.cancel = cancel
	c.cancelMu.Unlock()
}

func (c *Claude) args() []string {
	sys := systemPrompt
	if c.opts.Context != "" {
		sys += "\n\nReview context:\n" + c.opts.Context
	}
	args := []string{"-p", "--output-format", "stream-json", "--include-partial-messages", "--verbose",
		"--permission-mode", "plan", "--append-system-prompt", sys}
	args = append(args, "--allowedTools")
	args = append(args, readOnlyTools...)
	args = append(args, "--disallowedTools")
	args = append(args, deniedTools...)
	if c.opts.Model != "" {
		args = append(args, "--model", c.opts.Model)
	}
	if c.started {
		return append(args, "--resume", c.sessionID)
	}
	return append(args, "--session-id", c.sessionID)
}

// streamEvent is the subset of claude's stream-json output revdiff reads.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	Event   struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
}

type streamResult struct {
	done bool  // a result event arrived
	err  error // the result reported an error
}

// readStream forwards text deltas to onChunk and records the final result. The
// result text is only forwarded when no deltas arrived, so an answer is never
// shown twice.
func (c *Claude) readStream(r io.Reader, onChunk func(string)) streamResult {
	var res streamResult
	sawText := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var ev streamEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue // claude prints the odd non-JSON warning line
		}
		switch {
		case ev.Type == "system" && ev.Subtype == "init":
			c.started = true // the session exists from here on, even if this turn fails
		case ev.Type == "stream_event" && ev.Event.Type == "content_block_delta" && ev.Event.Delta.Type == "text_delta":
			sawText = true
			onChunk(ev.Event.Delta.Text)
		case ev.Type == "result":
			res.done = true
			if ev.IsError || ev.Subtype != "success" {
				res.err = fmt.Errorf("ask: claude: %s", firstNonEmpty(ev.Result, ev.Subtype))
				continue
			}
			if !sawText && ev.Result != "" {
				onChunk(ev.Result)
			}
		}
	}
	return res
}

// newSessionID returns a random version 4 UUID, the form claude requires.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return "unknown error"
}
