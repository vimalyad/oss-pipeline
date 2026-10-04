package watch

import "github.com/vimalyad/oss-pipeline/engine/internal/model"

// PRState reduces one poll, and the candidate's own status, to the schema's
// pr_state: the single word the dashboard shows for a pull request.
//
// The order is the order of what a person should do about it. GitHub's
// verdict on merge and close is final, so it comes first. A push we are
// making right now outranks anything we observed before it. After that, the
// states that block a merge regardless of what reviewers think -- a draft, a
// conflict, red CI -- come before the reviewers' verdict, because an approval
// on a pull request that cannot merge is not something a person can act on.
// Pending checks are below a review verdict for the same reason in reverse:
// "changes requested" is still true when the next CI run finishes.
//
// Stale sits low. A pull request nobody has touched in months but whose CI
// is red is better described by the red CI, which is something we can fix.
//
// blocked_needs_human is not derived here. It is a claim about the
// pipeline's own state, not GitHub's, and nothing in a poll can make it.
func PRState(st State, status model.Status) string {
	switch {
	case st.Merged || st.State == "MERGED":
		return "merged"
	case st.State == "CLOSED":
		return "closed"
	case status == model.StatusUpdating:
		return "updating"
	case st.IsDraft:
		return "draft"
	case st.Mergeable == "CONFLICTING":
		return "conflicted"
	case len(st.Failing) > 0:
		return "ci_failing"
	case st.ReviewDecision == "CHANGES_REQUESTED":
		return "changes_requested"
	case st.ReviewDecision == "APPROVED":
		return "approved"
	// Checks held at ACTION_REQUIRED are waiting on a maintainer to let a
	// first-time contributor's workflows run. Not a failure; pending.
	case st.ChecksPending > 0 || len(st.Awaiting) > 0:
		return "ci_pending"
	case status == model.StatusStale:
		return "stale"
	case st.Reviewers > 0:
		return "under_review"
	case st.ReviewDecision == "REVIEW_REQUIRED":
		return "review_required"
	}
	return "open"
}
