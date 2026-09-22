package model

import (
	"errors"
	"testing"
)

// TestEveryLiveStatusReachesTerminal is the invariant, derived rather than
// enumerated so the hole cannot reopen when a status is added.
//
// GitHub decides merge and close, not this pipeline. v1 omitted
// ChangesRequested -> Merged while a real PR sat in exactly that state; the
// error was caught by a bare handler and the merge would have been lost.
func TestEveryLiveStatusReachesTerminal(t *testing.T) {
	for _, live := range OpenStatuses {
		allowed, ok := Transitions[live]
		if !ok {
			t.Fatalf("%s is in OpenStatuses but not in Transitions", live)
		}
		if !allowed[StatusMerged] {
			t.Errorf("%s cannot record a merge", live)
		}
		if !allowed[StatusClosed] {
			t.Errorf("%s cannot record a close", live)
		}
	}
}

// TestHumanGateIsStructurallyUnreachable: no run reaches Implementing by
// drifting through the table. This is a property of the table, not of a flag,
// so no amount of --execute can bypass it.
//
// Exactly two statuses reach Implementing. Approved is written only by a
// person; AutoApproved only by internal/autogate, which
// TestOnlyAutogateWritesAutoApproved enforces against the source. Adding the
// autonomous path as its own labelled edge rather than as a second way into
// Approved is what keeps every pull request attributable to one or the other
// from the audit log alone.
func TestHumanGateIsStructurallyUnreachable(t *testing.T) {
	for _, from := range []Status{StatusDiscovered, StatusScored, StatusProposed} {
		if Transitions[from][StatusImplementing] {
			t.Errorf("%s reaches Implementing without an approval step", from)
		}
	}
	var inbound []Status
	for s, outs := range Transitions {
		if outs[StatusImplementing] {
			inbound = append(inbound, s)
		}
	}
	sortStatuses(inbound)
	want := []Status{StatusApproved, StatusAutoApproved}
	if len(inbound) != len(want) || inbound[0] != want[0] || inbound[1] != want[1] {
		t.Fatalf("Implementing reachable from %v; want exactly %v", inbound, want)
	}
}

// TestAutoApprovedHasExactlyOneWayIn: an autonomous approval must come from a
// scored, proposed candidate and from nowhere else. A second inbound edge
// would be a route into the autonomous path that skips the scoring the
// autonomy guardrails are built on top of.
func TestAutoApprovedHasExactlyOneWayIn(t *testing.T) {
	var inbound []Status
	for s, outs := range Transitions {
		if outs[StatusAutoApproved] {
			inbound = append(inbound, s)
		}
	}
	if len(inbound) != 1 || inbound[0] != StatusProposed {
		t.Fatalf("AutoApproved reachable from %v; want only [proposed]", inbound)
	}
	// And it must never be a way to launder a human rejection.
	if Transitions[StatusRejected][StatusAutoApproved] {
		t.Error("a rejected candidate can be auto-approved")
	}
}

func sortStatuses(ss []Status) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}

// TestIllegalTransitionIsADistinctError: the reason this is its own error
// value is that v1 caught it alongside network failures, making a bug in our
// own ordering indistinguishable from a timeout.
func TestIllegalTransitionIsADistinctError(t *testing.T) {
	c := &Candidate{Repo: "a/b", Issue: 1, Status: StatusProposed}
	err := Transition(c, StatusMerged, "")
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("want ErrIllegalTransition, got %v", err)
	}
	if c.Status != StatusProposed {
		t.Error("a refused transition must not mutate the candidate")
	}
	if len(c.History) != 0 {
		t.Error("a refused transition must not write history")
	}
}

func TestTransitionRecordsHistory(t *testing.T) {
	c := &Candidate{Repo: "a/b", Issue: 1, Status: StatusChangesRequested}
	if err := Transition(c, StatusMerged, "merged upstream"); err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusMerged || len(c.History) != 1 {
		t.Fatalf("status=%s history=%d", c.Status, len(c.History))
	}
	h := c.History[0]
	if h.From != "changes_requested" || h.To != "merged" || h.Note != "merged upstream" {
		t.Errorf("bad history entry: %+v", h)
	}
	if h.At == "" {
		t.Error("history needs a real timestamp; v1 had entries with 'retry' in that field")
	}
	if h.Forced {
		t.Error("an ordinary transition must not be marked forced")
	}
}

func TestReopenIsTheOnlySanctionedBypass(t *testing.T) {
	c := &Candidate{Repo: "a/b", Issue: 1, Status: StatusAbandoned}
	// Abandoned is terminal for the ordinary path.
	if err := Transition(c, StatusApproved, ""); !errors.Is(err, ErrIllegalTransition) {
		t.Fatal("Abandoned must be terminal for Transition")
	}
	if err := Reopen(c, StatusApproved, "cli", "toolchain installed"); err != nil {
		t.Fatalf("this edge is sanctioned: %v", err)
	}
	if !c.History[0].Forced || c.History[0].Actor != "cli" {
		t.Errorf("a bypass must be labelled and attributed: %+v", c.History[0])
	}
	// But only the listed edges.
	d := &Candidate{Repo: "a/b", Issue: 2, Status: StatusMerged}
	if err := Reopen(d, StatusApproved, "cli", "no"); !errors.Is(err, ErrIllegalTransition) {
		t.Error("Reopen must not accept arbitrary edges")
	}
}

func TestSlugMatchesV1Filenames(t *testing.T) {
	c := &Candidate{Repo: "kornia/kornia", Issue: 4201}
	if got := c.Slug(); got != "kornia__kornia__4201" {
		t.Fatalf("slug = %q; v1 filenames would stop resolving", got)
	}
}

func TestTargetable(t *testing.T) {
	for c, want := range map[Contest]bool{
		ContestNoPR: true, ContestStalePR: true,
		ContestActivePR: false, ContestClaimed: false,
	} {
		if c.Targetable() != want {
			t.Errorf("%s targetable = %v, want %v", c, c.Targetable(), want)
		}
	}
}

// TestAnApprovalCanBeWithdrawn: a person may change their mind before
// implementation starts, and `pipeline exclude` must be able to drop what is
// already queued. v1 had no such edge and swallowed the error, so excluding a
// repository left its approved candidates to run on the next cycle.
func TestAnApprovalCanBeWithdrawn(t *testing.T) {
	for _, from := range []Status{StatusProposed, StatusApproved, StatusAutoApproved} {
		if !Transitions[from][StatusRejected] {
			t.Errorf("%s cannot be rejected, so exclude cannot drop it", from)
		}
	}
	// Withdrawal must not be possible once work has started or shipped:
	// those need abandon or the real outcome, not a quiet rejection.
	for _, from := range []Status{StatusImplementing, StatusPROpen, StatusMerged, StatusClosed} {
		if Transitions[from][StatusRejected] {
			t.Errorf("%s can be rejected, which would hide a real outcome", from)
		}
	}
}
