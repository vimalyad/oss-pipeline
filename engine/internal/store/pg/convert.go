package pg

import (
	"strings"
	"time"

	"github.com/vimalyad/oss-pipeline/engine/internal/model"
)

// arr coerces a nil slice to an empty one.
//
// A nil Go slice is sent as SQL NULL, which overrides a column's
// `NOT NULL DEFAULT '{}'` rather than falling back to it -- so every
// candidate with no labels failed to insert, with an error naming whichever
// array column the planner reached first. The model uses nil and empty
// interchangeably; the schema does not, and the schema is right to.
func arr[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// nullStr maps the model's empty string onto SQL NULL.
//
// The distinction matters in one place and is worth honouring everywhere: a
// candidate with no branch and a candidate whose branch is "" are the same
// thing to this engine, and NULL is what the schema's own constraints are
// written against.
func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// nullTime parses the model's RFC 3339 strings.
//
// An unparseable value becomes NULL rather than an error. The predecessor
// carried four history entries whose timestamp was the literal string
// "retry", written by hand before a sanctioned reopen edge existed; they were
// silently skipped by every date calculation. Refusing to load them would
// strand the candidates, and inventing a time would be worse, so the absence
// is recorded as an absence.
func nullTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// rejection splits the model's single reason string into the kind the schema
// wants and the text a person reads.
//
// The kind is load-bearing: a human decision is never revisited, a structural
// one can never become true, and a transient one comes back for another look.
// The predecessor kept this distinction in a prefix and a substring match
// over the reason text, which is why a scorer deferral and a considered
// refusal were indistinguishable after one sweep rewrote the file.
func rejection(c *model.Candidate) (kind *string, reason string) {
	if c.Status != model.StatusRejected {
		return nil, c.RejectReason
	}
	reason = c.RejectReason
	k := classify(reason)
	return &k, reason
}

func classify(reason string) string {
	low := strings.ToLower(reason)
	switch {
	case strings.HasPrefix(low, "human rejection"):
		return "human"
	case containsAny(low,
		"manually excluded", "touched by another of your accounts",
		"bans ai", "requires ai disclosure", "cannot be verified",
		"workflow changes are excluded", "cla"):
		return "structural"
	case containsAny(low,
		"max_harvest", "deferred", "contest=", "claimed by",
		"rate limit", "harvest failed", "brief failed", "unavailable"):
		return "transient"
	default:
		// Everything the scorer decided on a configurable bar. It comes back
		// if the bar moves, which is the honest default for a verdict this
		// engine reached rather than a person.
		return "quality"
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
