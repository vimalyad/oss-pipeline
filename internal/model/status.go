// Package model holds the shapes shared across every stage, and the candidate
// state machine that governs what may happen to a candidate next.
package model

import "fmt"

// Status is a candidate's position in the pipeline. The string values are the
// on-disk representation and must not change: v1 wrote 224 of these files.
type Status string

const (
	StatusDiscovered       Status = "discovered"
	StatusScored           Status = "scored"
	StatusProposed         Status = "proposed"
	StatusApproved         Status = "approved"
	StatusRejected         Status = "rejected"
	StatusImplementing     Status = "implementing"
	StatusImplemented      Status = "implemented"
	StatusAbandoned        Status = "abandoned"
	StatusPushed           Status = "pushed"
	StatusPROpen           Status = "pr_open"
	StatusChangesRequested Status = "changes_requested"
	StatusUpdating         Status = "updating"
	StatusMerged           Status = "merged"
	StatusClosed           Status = "closed"
	StatusStale            Status = "stale"
)

// OpenStatuses are the states in which GitHub, not this pipeline, owns the
// outcome. Every one of them must be able to reach both terminal results --
// see the invariant enforced in TestEveryLiveStatusReachesTerminal.
//
// StatusStale belongs here and was missing from v1's equivalent set. Nothing
// in v1 ever assigned it, so the omission was latent; watch now does, and a
// stale pull request is still open on GitHub. Leaving it out would have let a
// stale PR escape the open-PR cap -- the pipeline would believe it had room
// for another and maintainers would see more open PRs than the cap allows --
// and would have undercounted the ledger's open column.
var OpenStatuses = []Status{
	StatusPushed, StatusPROpen, StatusChangesRequested, StatusUpdating, StatusStale,
}

// Transitions is the whole state machine, written out rather than derived.
//
// The human gate is structural: there is deliberately no edge from Scored or
// Proposed to Implementing, so an unattended run cannot reach Implementing
// without a human having moved the candidate to Approved.
var Transitions = map[Status]map[Status]bool{
	StatusDiscovered:   set(StatusScored, StatusRejected),
	StatusScored:       set(StatusProposed, StatusRejected),
	StatusProposed:     set(StatusApproved, StatusRejected),
	StatusApproved:     set(StatusImplementing, StatusAbandoned),
	StatusImplementing: set(StatusImplemented, StatusAbandoned),
	StatusImplemented:  set(StatusPushed, StatusAbandoned),

	// GitHub -- not this pipeline -- decides merge and close, so every status
	// the watcher can observe must accept both. v1 omitted
	// ChangesRequested -> Merged while a real PR sat in exactly that state;
	// the resulting error was swallowed and the merge would have been lost.
	StatusPushed: set(StatusPROpen, StatusAbandoned, StatusMerged, StatusClosed),
	StatusPROpen: set(StatusChangesRequested, StatusUpdating, StatusMerged,
		StatusClosed, StatusStale),
	// PROpen is reachable again because a maintainer can dismiss their own
	// review, which returns the PR to plain open.
	StatusChangesRequested: set(StatusUpdating, StatusPROpen, StatusMerged,
		StatusClosed, StatusStale),
	StatusUpdating: set(StatusPROpen, StatusAbandoned, StatusMerged,
		StatusClosed, StatusStale),
	StatusStale: set(StatusPROpen, StatusUpdating, StatusMerged, StatusClosed),

	StatusRejected:  set(),
	StatusAbandoned: set(),
	StatusMerged:    set(),
	StatusClosed:    set(),
}

func set(ss ...Status) map[Status]bool {
	m := make(map[Status]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

// Valid reports whether s is a status this pipeline knows. Used when loading
// v1's files: an unknown status means the file was hand-edited wrongly, and we
// would rather say so than carry on with a zero value.
func (s Status) Valid() bool {
	_, ok := Transitions[s]
	return ok
}

func (s Status) String() string { return string(s) }

// Contest is how contested an issue is, decided by objective signals only --
// never a judgement about the quality of someone else's code.
type Contest string

const (
	ContestNoPR     Contest = "no_pr"
	ContestStalePR  Contest = "stale_pr"
	ContestActivePR Contest = "active_pr"
	ContestClaimed  Contest = "claimed"
)

// Targetable reports whether we may open a PR against this contest class.
func (c Contest) Targetable() bool {
	return c == ContestNoPR || c == ContestStalePR
}

func (c Contest) String() string { return string(c) }

// ErrIllegalTransition is returned when a state change is not in Transitions.
//
// It is a distinct error value on purpose. In v1 this was an exception caught
// by a bare `except Exception` alongside network failures, which is how a lost
// merge could look identical to a timeout. Callers must use errors.Is and
// treat it as a bug in their own ordering, not as a transient fault.
var ErrIllegalTransition = fmt.Errorf("illegal state transition")
