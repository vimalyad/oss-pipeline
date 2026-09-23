// Package llm runs the `claude` CLI for the judgement steps.
//
// A subprocess rather than an SDK, for the same reason GitHub goes through
// `gh`: no API key plumbing on the host, and the CLI already handles auth,
// retries and model selection.
//
// Two kinds of call, and the difference is a security boundary:
//
//	Judge  -- reads text we pass in, with every tool disabled. It cannot read
//	          a file, run a command, or touch the network. Used for anything
//	          that looks at a third-party thread or diff.
//	Agent  -- writes code. Runs inside a container (see internal/sandbox),
//	          never on the host, because it executes the target repo's own
//	          build commands.
//
// Only Judge lives here. Agent belongs with the sandbox that confines it.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/vimalyad/osspipeline/internal/text"
)

// ErrLLM is any failure to get a usable answer.
var ErrLLM = errors.New("llm")

// Model names. Judgement work uses the cheaper model; only code generation
// justifies the larger one.
const (
	ModelJudge = "sonnet"
	ModelCode  = "opus"
)

// DefaultTimeout is generous because these calls queue behind each other in an
// unattended run, and a killed call costs a whole stage.
const DefaultTimeout = 5 * time.Minute

type Client struct {
	// Env is deliberately NOT the identity environment: a judgement call has
	// no business holding a GitHub token.
	Env     []string
	Timeout time.Duration
	Log     func(string)
	// exec is swappable for tests.
	exec func(ctx context.Context, args []string, stdin string, env []string) (string, string, error)
}

func New(env []string) *Client {
	return &Client{Env: env, Timeout: DefaultTimeout, exec: runClaude}
}

func (c *Client) logf(f string, a ...any) {
	if c.Log != nil {
		c.Log(fmt.Sprintf(f, a...))
	}
}

func runClaude(ctx context.Context, args []string, stdin string, env []string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Env = env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// Judge asks a question about text, with every tool disabled.
//
// The tool restrictions are the point. This call is routinely handed a
// third-party issue thread or a diff, which is untrusted input: anything in it
// that reads like an instruction must not be able to reach a file or a shell.
func (c *Client) Judge(ctx context.Context, prompt string) (string, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{
		"-p", prompt,
		"--model", ModelJudge,
		"--disallowed-tools", "Read", "Write", "Edit", "Bash", "WebFetch", "WebSearch",
	}
	out, errOut, err := c.exec(ctx, args, "", c.Env)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%w: timed out after %s", ErrLLM, timeout)
		}
		return "", fmt.Errorf("%w: %v: %s", ErrLLM, err, text.Ellipsis(errOut, 300))
	}
	return strings.TrimSpace(out), nil
}

// JudgeJSON runs Judge and decodes the answer into v.
//
// Models wrap JSON in prose or fences often enough that extracting it is the
// normal path, not error handling. One retry, because a second attempt with an
// explicit complaint usually succeeds and a failed judgement blocks a stage.
func (c *Client) JudgeJSON(ctx context.Context, prompt string, v any) error {
	attempt := func(p string) error {
		out, err := c.Judge(ctx, p)
		if err != nil {
			return err
		}
		blob, err := ExtractJSON(out)
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(blob), v); err != nil {
			return fmt.Errorf("%w: response was not the expected shape: %v", ErrLLM, err)
		}
		return nil
	}
	err := attempt(prompt)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "timed out") {
		return err
	}
	c.logf("    llm: retrying after unusable answer (%v)", err)
	return attempt(prompt + "\n\nReturn ONLY the JSON object. No prose, no code fences.")
}

// ExtractJSON pulls the first JSON object or array out of a model answer.
func ExtractJSON(s string) (string, error) {
	s = strings.TrimSpace(s)
	if fenced := betweenFences(s); fenced != "" {
		s = fenced
	}
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return "", fmt.Errorf("%w: no JSON in answer: %s", ErrLLM, text.Ellipsis(s, 200))
	}
	open := s[start]
	close := byte('}')
	if open == '[' {
		close = ']'
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		ch := s[i]
		switch {
		case esc:
			esc = false
		case ch == '\\' && inStr:
			esc = true
		case ch == '"':
			inStr = !inStr
		case inStr:
			// nothing: braces inside strings must not move the depth
		case ch == open:
			depth++
		case ch == close:
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("%w: unterminated JSON in answer: %s", ErrLLM, text.Ellipsis(s, 200))
}

func betweenFences(s string) string {
	i := strings.Index(s, "```")
	if i < 0 {
		return ""
	}
	rest := s[i+3:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}
	if j := strings.Index(rest, "```"); j >= 0 {
		return strings.TrimSpace(rest[:j])
	}
	return ""
}

// Render fills a prompt template. Deliberately minimal: {{key}} substitution
// with no logic, so a prompt stays readable as prose.
func Render(tmpl string, vars map[string]string) string {
	for k, v := range vars {
		tmpl = strings.ReplaceAll(tmpl, "{{"+k+"}}", v)
	}
	return tmpl
}
