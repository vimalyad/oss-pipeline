// Package replies drafts responses to maintainer feedback for the author to
// approve and post.
//
// The autonomy split says mechanical fixes go out automatically but anything a
// human would say stays human-approved. That only works if a draft actually
// exists: queuing "needs a reply" with no text leaves the maintainer waiting
// and the author with a blank page, and a pushed fix nobody announces reads,
// from the reviewer's side, as no response at all.
//
// Nothing here posts anything. Post is called only by an explicit command.
package replies

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/vimalyad/osspipeline/internal/guard"
	"github.com/vimalyad/osspipeline/internal/llm"
	"github.com/vimalyad/osspipeline/internal/model"

	"github.com/vimalyad/osspipeline/internal/text"
)

//go:embed prompts/reply.md
var replyPrompt string

var (
	ErrNoDraft = errors.New("no usable draft")
	ErrReplies = errors.New("replies")
)

// Drafter turns a prompt into reply text.
type Drafter interface {
	Judge(ctx context.Context, prompt string) (string, error)
}

// Commenter posts a comment on a pull request. Separate from Drafter so the
// thing that writes and the thing that publishes are never the same object.
type Commenter interface {
	Comment(ctx context.Context, repo string, pr int, body string) (string, error)
}

// Context is what a draft has to be checked against.
//
// The diff is not optional. A draft written from commit subjects alone once
// announced three fixes as "not yet done" when the diff already contained all
// three -- which, posted, would have been three false statements to a
// maintainer under the user's name.
type Context struct {
	Commits string
	Diff    string
}

const (
	maxDiff     = 50000
	maxFeedback = 3000
)

// Pending returns the queued items that have not been posted, with their index
// in the candidate's own list so a caller can address one.
func Pending(c *model.Candidate) []int {
	var out []int
	for i, q := range c.QueuedReplies {
		if b, _ := q["posted"].(bool); !b {
			out = append(out, i)
		}
	}
	return out
}

// Draft writes a reply for one queued item and stores it on the candidate.
func Draft(ctx context.Context, d Drafter, c *model.Candidate, idx int, rc Context) (string, error) {
	if idx < 0 || idx >= len(c.QueuedReplies) {
		return "", fmt.Errorf("%w: no queued item %d", ErrReplies, idx)
	}
	item := c.QueuedReplies[idx]

	pr := 0
	if c.PRNumber != nil {
		pr = *c.PRNumber
	}
	prompt := llm.Render(replyPrompt, map[string]string{
		"repo": c.Repo, "pr": fmt.Sprint(pr), "title": c.Title,
		"commits":  orElse(rc.Commits, "(nothing pushed since)"),
		"diff":     orElse(text.Clip(rc.Diff, maxDiff), "(diff unavailable)"),
		"feedback": text.Clip(str(item, "body"), maxFeedback),
	})

	text, err := d.Judge(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrReplies, err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("%w: the drafter returned nothing", ErrReplies)
	}

	item["draft"] = text
	// The forbidden text is kept rather than discarded. v1 replaced the whole
	// draft with a marker, which left the author a blank page and no way to
	// see what the model had actually written; keeping it means a two-word
	// edit instead of starting over. Post refuses while the flags are set, so
	// nothing reaches GitHub on the strength of this.
	if bad := guard.CheckBody(text); len(bad) > 0 {
		item["draft_rejected"] = toAny(bad)
	} else {
		delete(item, "draft_rejected")
	}
	return text, nil
}

// DraftAll fills in every pending item that has no draft yet, and reports how
// many it wrote. A failure on one item does not stop the rest: a maintainer
// waiting on item three should not go unanswered because item one upset the
// model.
func DraftAll(ctx context.Context, d Drafter, c *model.Candidate, rc Context) (int, error) {
	var n int
	var errs []string
	for _, i := range Pending(c) {
		if str(c.QueuedReplies[i], "draft") != "" {
			continue
		}
		if _, err := Draft(ctx, d, c, i, rc); err != nil {
			errs = append(errs, fmt.Sprintf("item %d: %v", i, err))
			continue
		}
		n++
	}
	if len(errs) > 0 {
		return n, fmt.Errorf("%w: %s", ErrReplies, strings.Join(errs, "; "))
	}
	return n, nil
}

// Usable reports whether an item can be posted, and why not when it cannot.
func Usable(item map[string]any) (bool, string) {
	if b, _ := item["posted"].(bool); b {
		return false, "already posted"
	}
	if str(item, "draft") == "" {
		return false, "no draft yet -- run the draft step first"
	}
	if bad := fromAny(item["draft_rejected"]); len(bad) > 0 {
		return false, "the draft mentions " + strings.Join(bad, ", ") +
			"; edit it in the candidate file and try again"
	}
	return true, ""
}

// Post publishes one drafted reply.
//
// It re-checks the text immediately before sending. The draft on disk is
// hand-editable by design, and the check that ran when it was written says
// nothing about what is there now.
func Post(ctx context.Context, cm Commenter, c *model.Candidate, idx int) (string, error) {
	if idx < 0 || idx >= len(c.QueuedReplies) {
		return "", fmt.Errorf("%w: no queued item %d", ErrReplies, idx)
	}
	item := c.QueuedReplies[idx]
	if ok, why := Usable(item); !ok {
		return "", fmt.Errorf("%w: item %d: %s", ErrNoDraft, idx, why)
	}
	text := str(item, "draft")
	if bad := guard.CheckBody(text); len(bad) > 0 {
		item["draft_rejected"] = toAny(bad)
		return "", fmt.Errorf("%w: item %d mentions %s", ErrNoDraft, idx, strings.Join(bad, ", "))
	}
	if c.PRNumber == nil {
		return "", fmt.Errorf("%w: candidate has no PR", ErrReplies)
	}

	out, err := cm.Comment(ctx, c.Repo, *c.PRNumber, text)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrReplies, err)
	}
	item["posted"] = true
	item["posted_url"] = strings.TrimSpace(out)
	return strings.TrimSpace(out), nil
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// toAny and fromAny keep the on-disk shape JSON-round-trippable: a []string
// written today comes back as []any after a reload, and code that assumed
// otherwise would break only on the second run.
func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func fromAny(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{t}
	}
	return nil
}

func orElse(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
