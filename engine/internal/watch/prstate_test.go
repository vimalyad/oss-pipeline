package watch

import (
	"testing"

	"github.com/vimalyad/oss-pipeline/engine/internal/model"
)

func TestPRStateFollowsWhatAPersonShouldDoFirst(t *testing.T) {
	red := []Check{{Name: "test"}}
	for _, tc := range []struct {
		name   string
		st     State
		status model.Status
		want   string
	}{
		{"merged beats everything", State{Merged: true, Failing: red, IsDraft: true}, model.StatusUpdating, "merged"},
		{"closed beats red CI", State{State: "CLOSED", Failing: red}, model.StatusPROpen, "closed"},
		{"our push in flight", State{Failing: red}, model.StatusUpdating, "updating"},
		{"draft", State{IsDraft: true, Failing: red}, model.StatusPROpen, "draft"},
		{"conflict before red CI", State{Mergeable: "CONFLICTING", Failing: red}, model.StatusPROpen, "conflicted"},
		{"red CI before an approval", State{Failing: red, ReviewDecision: "APPROVED"}, model.StatusPROpen, "ci_failing"},
		{"changes requested", State{ReviewDecision: "CHANGES_REQUESTED", ChecksPending: 2}, model.StatusPROpen, "changes_requested"},
		{"approved", State{ReviewDecision: "APPROVED", ChecksPending: 1}, model.StatusPROpen, "approved"},
		{"pending checks", State{ChecksPending: 1}, model.StatusPROpen, "ci_pending"},
		{"held workflows are pending, not failing", State{Awaiting: red}, model.StatusPROpen, "ci_pending"},
		{"stale", State{Reviewers: 1}, model.StatusStale, "stale"},
		{"someone reviewed", State{Reviewers: 1}, model.StatusPROpen, "under_review"},
		{"project requires review", State{ReviewDecision: "REVIEW_REQUIRED"}, model.StatusPROpen, "review_required"},
		{"nothing yet", State{State: "OPEN"}, model.StatusPROpen, "open"},
	} {
		if got := PRState(tc.st, tc.status); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

// Observe is told about the settled cycle, not the poll: a merged pull request
// must reach the store with the candidate already merged.
func TestObserveSeesTheSettledStatus(t *testing.T) {
	var saw model.Status
	n := 7
	c := &model.Candidate{Repo: "a/b", Issue: 1, Status: model.StatusPROpen, PRNumber: &n}
	d := Deps{
		API: &fakeAPI{resp: `{"repository":{"pullRequest":{"number":7,"state":"MERGED","merged":true}}}`},
		Observe: func(c *model.Candidate, st State) error {
			saw = c.Status
			return nil
		},
	}
	if _, err := Sync(t.Context(), d, c, false); err != nil {
		t.Fatal(err)
	}
	if saw != model.StatusMerged {
		t.Fatalf("observe saw %q, want merged", saw)
	}
}
