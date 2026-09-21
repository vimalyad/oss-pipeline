package watch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vimalyad/osspipeline/internal/model"
	"github.com/vimalyad/osspipeline/internal/notify"
)

// Fixer applies the mechanical fixes and pushes them. It returns whether
// anything was pushed.
type Fixer interface {
	Fix(ctx context.Context, c *model.Candidate, items []Item) (pushed bool, err error)
}

// Saver persists a candidate.
type Saver interface {
	Save(c *model.Candidate) (string, error)
}

// Deps are the collaborators one watch cycle needs. Each is the narrowest
// interface that does the job, so a test supplies four small fakes rather than
// a GitHub client.
type Deps struct {
	API      API
	Checks   CheckClassifier
	Feedback FeedbackClassifier
	Store    Saver
	Fixer    Fixer
	Audit    func(kind, slug, detail string)
	Notify   func(notify.Event)
	Now      func() time.Time
	// StaleAfterDays marks a PR stale when nothing has happened for this long.
	// Zero disables it.
	StaleAfterDays int
}

// Outcome is what one cycle did, in the form a digest line wants.
type Outcome struct {
	Status  model.Status
	Summary string
	Pushed  bool
	Queued  int
	State   State
}

// Sync runs one watch cycle for one pull request.
//
// execute=false is a dry run: it reports what it found and changes no state on
// GitHub, but it still records the poll, because knowing what a cycle would do
// is worthless if the next one re-discovers the same items.
func Sync(ctx context.Context, d Deps, c *model.Candidate, execute bool) (Outcome, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if c.PRNumber == nil {
		return Outcome{Status: c.Status, Summary: "no PR"}, nil
	}

	st, err := Poll(ctx, d.API, c)
	if err != nil {
		return Outcome{Status: c.Status}, err
	}
	if d.Checks != nil {
		ClassifyChecks(ctx, d.Checks, &st, seenSet(c))
	}
	if d.Feedback != nil {
		// A classification failure is not a cycle failure: the items fall
		// back to needs_reply and the human sees them.
		_ = ClassifyFeedback(ctx, d.Feedback, &st)
	}

	// GitHub decides merge and close, not this pipeline, so those two come
	// first and nothing else in the cycle can talk us out of them.
	if st.Merged {
		if err := d.move(c, model.StatusMerged, "merged upstream"); err != nil {
			return Outcome{Status: c.Status, State: st}, err
		}
		d.record("merged", c.Slug(), c.PRURL)
		d.send(notify.Merged(c.Slug(), c.Repo, c.Issue, c.PRURL))
		d.save(c)
		return Outcome{Status: c.Status, Summary: "MERGED", State: st}, nil
	}
	if st.State == "CLOSED" {
		if err := d.move(c, model.StatusClosed, "closed without merge"); err != nil {
			return Outcome{Status: c.Status, State: st}, err
		}
		d.record("closed", c.Slug(), c.PRURL)
		d.send(notify.Closed(c.Slug(), c.Repo, c.Issue, c.PRURL))
		d.save(c)
		return Outcome{Status: c.Status, Summary: "closed without merge", State: st}, nil
	}

	b := st.Buckets()
	out := Outcome{State: st}

	if mech := b[ClassMechanical]; len(mech) > 0 && d.Fixer != nil && execute {
		if err := d.move(c, model.StatusUpdating, fmt.Sprintf("%d mechanical item(s)", len(mech))); err != nil {
			return Outcome{Status: c.Status, State: st}, err
		}
		pushed, ferr := d.Fixer.Fix(ctx, c, mech)
		out.Pushed = pushed
		switch {
		case ferr != nil:
			// The fix attempt failed. Returning to PR_OPEN rather than
			// staying in UPDATING matters: UPDATING is a claim that a push is
			// in flight, and a stuck claim blocks the next cycle forever.
			_ = d.move(c, model.StatusPROpen, "fix attempt failed: "+ferr.Error())
			d.record("ci_fix_failed", c.Slug(), ferr.Error())
		case pushed:
			_ = d.move(c, model.StatusPROpen, "pushed a fix; awaiting the new checks")
			d.record("ci_fix_pushed", c.Slug(), st.HeadSHA)
		default:
			_ = d.move(c, model.StatusPROpen, "nothing to push")
		}
	}

	queued := st.Queued()
	if len(queued) > 0 {
		for _, it := range queued {
			c.QueuedReplies = append(c.QueuedReplies, map[string]any{
				"id": it.ID, "author": it.Author, "kind": it.Kind, "body": it.Body,
				"url": it.URL, "cls": string(it.Class), "why": it.Why, "action": it.Action,
			})
		}
		out.Queued = len(queued)
		if c.Status == model.StatusPROpen {
			if err := d.move(c, model.StatusChangesRequested,
				fmt.Sprintf("%d item(s) need a human", len(queued))); err != nil {
				return Outcome{Status: c.Status, State: st}, err
			}
		}
		for i, it := range queued {
			d.send(notify.ReplyNeeded(c.Slug(), c.Repo, c.Issue, c.PRURL, it.Author, it.Action, i))
		}
	}

	if d.StaleAfterDays > 0 && len(queued) == 0 && !out.Pushed {
		if days, ok := idleDays(st.UpdatedAt, d.Now()); ok && days >= d.StaleAfterDays {
			if c.Status == model.StatusPROpen || c.Status == model.StatusChangesRequested {
				if err := d.move(c, model.StatusStale,
					fmt.Sprintf("no activity for %d days", days)); err != nil {
					return Outcome{Status: c.Status, State: st}, err
				}
			}
		}
	}

	if execute {
		for _, it := range st.Items {
			c.WatchSeen = append(c.WatchSeen, it.ID)
		}
	}
	d.save(c)

	out.Status = c.Status
	out.Summary = summarize(b, st, out.Pushed)
	return out, nil
}

// move applies a transition, and makes an illegal one loud.
//
// v1 wrapped this call in a bare `except Exception` that logged nothing, so a
// bug in our own ordering was indistinguishable from a network blip -- and one
// really did hide a merged PR for days. A transition the table forbids is a
// defect in this package, so it wakes someone.
func (d Deps) move(c *model.Candidate, next model.Status, note string) error {
	err := model.Transition(c, next, note)
	if err == nil {
		return nil
	}
	if errors.Is(err, model.ErrIllegalTransition) {
		d.record("transition_error", c.Slug(), err.Error())
		d.send(notify.InternalError(c.Slug(), "state machine bug", err.Error()))
		return fmt.Errorf("watch %s: %w", c.Slug(), err)
	}
	return err
}

func (d Deps) save(c *model.Candidate) {
	if d.Store == nil {
		return
	}
	if _, err := d.Store.Save(c); err != nil {
		d.record("save_failed", c.Slug(), err.Error())
	}
}

func (d Deps) record(kind, slug, detail string) {
	if d.Audit != nil {
		d.Audit(kind, slug, detail)
	}
}

func (d Deps) send(e notify.Event) {
	if d.Notify != nil {
		d.Notify(e)
	}
}

// idleDays is how long the PR has been untouched, or false when GitHub gave us
// no usable timestamp.
func idleDays(updatedAt string, now time.Time) (int, bool) {
	t, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return 0, false
	}
	d := int(now.UTC().Sub(t).Hours() / 24)
	if d < 0 {
		return 0, false
	}
	return d, true
}

func summarize(b map[Class][]Item, st State, pushed bool) string {
	var parts []string
	if pushed {
		parts = append(parts, "auto-fixed")
	}
	var keys []string
	for k := range b {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	for _, k := range keys {
		if n := len(b[Class(k)]); n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, n))
		}
	}
	if n := len(st.Awaiting); n > 0 {
		parts = append(parts, fmt.Sprintf("CI awaiting maintainer approval (%d)", n))
	}
	if len(parts) == 0 {
		return "no change"
	}
	return strings.Join(parts, ", ")
}
