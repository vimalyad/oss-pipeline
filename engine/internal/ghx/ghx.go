// Package ghx is all GitHub access, always as the OSS identity.
//
// Everything goes through the `gh` CLI rather than a raw HTTP client: it
// already handles auth, pagination and API versioning, and it honours
// GH_TOKEN, which is how the other account on this machine is kept out.
//
// Secondary rate limits are the interesting failure mode. They are partly
// per-IP, and the user contributes manually from a second account on this same
// machine, so retrying blind makes things worse for both. Retry-After is
// honoured and backoff is exponential with full jitter.
package ghx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const MaxAttempts = 5

var (
	// ErrGh is any non-retryable failure from the gh CLI.
	ErrGh = errors.New("gh")
	// ErrRateLimited means the primary budget is gone, or we gave up retrying
	// a secondary limit. Callers should stop, not spin.
	ErrRateLimited = fmt.Errorf("%w: rate limited", ErrGh)
	// ErrNotFound distinguishes a missing resource from a broken call, so a
	// renamed repo skips that repo instead of killing the run.
	ErrNotFound = fmt.Errorf("%w: not found", ErrGh)
)

var secondaryHints = []string{
	"secondary rate limit", "abuse detection", "exceeded a secondary",
}

// transientHints are failures that clear on their own. Treating a dropped
// connection as fatal once took down an entire unattended daily run --
// discovery and ledger included -- for a blip that had cleared minutes later.
var transientHints = []string{
	" 500", " 502", " 503", " 504", "timeout", "error connecting",
	"connection reset", "could not resolve", "network is unreachable",
	"eof occurred", "tls handshake", "connection refused",
}

// Client runs gh with a fixed environment. The environment carries the token,
// so a Client is the capability to act as the OSS identity -- pass it
// explicitly rather than reaching for a global.
type Client struct {
	Env []string
	// Log receives progress notes (backoff, retries). nil is silent.
	Log func(string)
	// sleep is swappable so tests do not actually wait.
	sleep func(time.Duration)
}

func New(env []string) *Client {
	return &Client{Env: env, sleep: time.Sleep}
}

func (c *Client) logf(format string, a ...any) {
	if c.Log != nil {
		c.Log(fmt.Sprintf(format, a...))
	}
}

// run executes gh, retrying transient and secondary-limit failures.
func (c *Client) run(ctx context.Context, args []string, stdin string) (string, error) {
	sleep := c.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	var last string
	for attempt := 0; attempt < MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		cmd := exec.CommandContext(ctx, "gh", args...)
		cmd.Env = c.Env
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		var out, errb bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errb
		err := cmd.Run()
		if err == nil {
			return out.String(), nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		last = strings.TrimSpace(errb.String())
		low := strings.ToLower(last)

		switch {
		case containsAny(low, secondaryHints) || strings.Contains(low, "429"):
			d := backoff(attempt, last)
			c.logf("    secondary rate limit; backing off %.0fs", d.Seconds())
			sleep(d)
			continue
		case strings.Contains(low, "rate limit") && strings.Contains(low, "exceeded"):
			return "", fmt.Errorf("%w: %s", ErrRateLimited, last)
		case strings.Contains(low, "404") || strings.Contains(low, "not found"):
			return "", fmt.Errorf("%w: gh %s: %s", ErrNotFound, summarize(args), last)
		case containsAny(low, transientHints):
			d := backoff(attempt, last)
			c.logf("    transient failure; retrying in %.0fs", d.Seconds())
			sleep(d)
			continue
		default:
			return "", fmt.Errorf("%w %s: %s", ErrGh, summarize(args), last)
		}
	}
	return "", fmt.Errorf("%w: gh %s gave up after %d attempts: %s",
		ErrRateLimited, summarize(args), MaxAttempts, last)
}

// backoff honours Retry-After when GitHub sends one, else exponential with
// full jitter -- several stages can be retrying at once.
func backoff(attempt int, stderr string) time.Duration {
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(strings.ToLower(line), "retry-after:") {
			_, v, _ := strings.Cut(line, ":")
			if secs, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return time.Duration(secs * float64(time.Second))
			}
		}
	}
	base := math.Min(60, math.Pow(2, float64(attempt)))
	return time.Duration(base * (0.5 + rand.Float64()/2) * float64(time.Second))
}

// RESTOptions configure a REST call.
type RESTOptions struct {
	Method   string
	Paginate bool
	JQ       string
	Fields   map[string]string
}

// REST calls the REST API and returns the raw response body.
func (c *Client) REST(ctx context.Context, path string, o RESTOptions) (string, error) {
	args := []string{"api", path}
	if o.Method != "" && o.Method != "GET" {
		args = append(args, "--method", o.Method)
	}
	for k, v := range o.Fields {
		args = append(args, "-f", k+"="+v)
	}
	if o.Paginate {
		args = append(args, "--paginate")
	}
	if o.JQ != "" {
		args = append(args, "--jq", o.JQ)
	}
	out, err := c.run(ctx, args, "")
	return strings.TrimSpace(out), err
}

// RESTRaw runs an arbitrary gh invocation and returns its stdout, with the
// same retry and rate-limit handling as everything else here.
//
// REST covers the shape almost every caller wants. This exists for the two it
// cannot express: reading response *headers* (`gh api -i`, which is how the
// token's scopes are discovered, since GitHub answers a scope-less write with
// 404 rather than 403) and sending a JSON body on stdin (`--input -`).
func (c *Client) RESTRaw(ctx context.Context, args []string, stdin string) (string, error) {
	out, err := c.run(ctx, args, stdin)
	return strings.TrimSpace(out), err
}

// RESTInto calls the REST API and decodes into v.
func (c *Client) RESTInto(ctx context.Context, path string, o RESTOptions, v any) error {
	out, err := c.REST(ctx, path, o)
	if err != nil {
		return err
	}
	if out == "" {
		return nil
	}
	if o.Paginate {
		merged, err := mergePages(out)
		if err != nil {
			return err
		}
		return json.Unmarshal(merged, v)
	}
	return json.Unmarshal([]byte(out), v)
}

// mergePages joins the concatenated JSON arrays `gh --paginate` emits into one
// array. gh does not produce a single document, so this cannot be one Unmarshal.
func mergePages(out string) ([]byte, error) {
	dec := json.NewDecoder(strings.NewReader(out))
	var all []json.RawMessage
	for {
		var page json.RawMessage
		if err := dec.Decode(&page); err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, fmt.Errorf("merge pages: %w", err)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(page, &items); err == nil {
			all = append(all, items...)
		} else {
			all = append(all, page)
		}
	}
	return json.Marshal(all)
}

// GraphQL runs a query and decodes the `data` object into v.
func (c *Client) GraphQL(ctx context.Context, query string, vars map[string]any, v any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	out, err := c.run(ctx, []string{"api", "graphql", "--input", "-"}, string(body))
	if err != nil {
		return err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		return fmt.Errorf("%w: graphql response: %v", ErrGh, err)
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		for i, e := range env.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("%w: graphql: %s", ErrGh, strings.Join(msgs, "; "))
	}
	if v == nil {
		return nil
	}
	return json.Unmarshal(env.Data, v)
}

// Budget is what is left of each API allowance.
type Budget struct{ Core, GraphQL, Search int }

func (c *Client) RateBudget(ctx context.Context) (Budget, error) {
	var r struct {
		Resources struct {
			Core    struct{ Remaining int } `json:"core"`
			GraphQL struct{ Remaining int } `json:"graphql"`
			Search  struct{ Remaining int } `json:"search"`
		} `json:"resources"`
	}
	if err := c.RESTInto(ctx, "rate_limit", RESTOptions{}, &r); err != nil {
		return Budget{}, err
	}
	return Budget{r.Resources.Core.Remaining, r.Resources.GraphQL.Remaining,
		r.Resources.Search.Remaining}, nil
}

// Login is the account gh resolves to with this client's environment. Used by
// verify to prove the token in use is the OSS identity and not the ambient one.
func (c *Client) Login(ctx context.Context) (string, error) {
	out, err := c.REST(ctx, "user", RESTOptions{JQ: ".login"})
	return strings.TrimSpace(out), err
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func summarize(args []string) string {
	if len(args) > 3 {
		args = args[:3]
	}
	return strings.Join(args, " ")
}
