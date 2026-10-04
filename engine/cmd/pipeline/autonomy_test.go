package main

import (
	"testing"
	"time"

	"github.com/vimalyad/oss-pipeline/engine/internal/autogate"
	"github.com/vimalyad/oss-pipeline/engine/internal/model"
	"github.com/vimalyad/oss-pipeline/engine/internal/profile"
)

var autoNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) string { return autoNow.Add(-d).Format(time.RFC3339) }

// autoCand is a candidate on repo whose history ends in an autonomous
// approval `approvedAgo` before now, currently at status.
func autoCand(repo string, issue int, status model.Status, approvedAgo time.Duration) *model.Candidate {
	return &model.Candidate{
		Repo: repo, Issue: issue, Status: status,
		History: []model.HistoryEntry{
			{At: ago(approvedAgo + time.Hour), From: "scored", To: "proposed"},
			{At: ago(approvedAgo), From: "proposed", To: "auto_approved"},
		},
	}
}

// A proposal that clears every quality gate, so the only thing left to vary
// in these tests is the caps.
func cleanProposal() *model.Candidate {
	return &model.Candidate{
		Repo: "helm/helm", Issue: 7, Status: model.StatusProposed, Contest: model.ContestNoPR,
		Brief: &model.Brief{MaintainerDesiredApproach: "return the error instead of panicking"},
		Facts: &model.RepoFacts{Repo: "helm/helm", HasTests: true},
	}
}

func gateInput(c *model.Candidate, caps autogate.Caps, repoHeld bool) autogate.Input {
	since := autoNow.Add(-24 * time.Hour)
	return autogate.Input{
		Now: autoNow, Want: profile.AutonomyOpenPRs, Candidate: c,
		Domain:        profile.Domain{ID: "devops-go", Autonomy: profile.AutonomyOpenPRs},
		Grant:         standingGrant("devops-go", since, false, autoNow),
		Caps:          caps,
		RepoHasAutoPR: repoHeld,
	}
}

func TestStandingGrantIsLiveImmediatelyWithoutATrial(t *testing.T) {
	// Switched on an hour ago, no trial: the very next decision may act.
	in := gateInput(cleanProposal(), autogate.Caps{PerDay: 2, PerWeek: 5, MaxOpen: 10}, false)
	in.Grant = standingGrant("devops-go", autoNow.Add(-time.Hour), false, autoNow)
	d := autogate.Decide(in)
	if !d.Allowed {
		t.Fatalf("refused: %s", d.Why())
	}
}

// The bug the first real run found: autonomy.since is a date, read as UTC
// midnight, and an operator in India switching it on in the evening is still
// before that instant in UTC. "No trial" must not depend on the clock.
func TestNoTrialHoldsEvenWhenSinceIsAheadOfNow(t *testing.T) {
	in := gateInput(cleanProposal(), autogate.Caps{PerDay: 2, PerWeek: 5, MaxOpen: 10}, false)
	in.Grant = standingGrant("devops-go", autoNow.Add(6*time.Hour), false, autoNow)
	if d := autogate.Decide(in); !d.Allowed {
		t.Fatalf("a no-trial grant was refused: %s", d.Why())
	}
}

// A trial's refusal means "not yet". Rejecting on it would permanently throw
// away exactly the proposals the trial exists to observe -- which the first
// real run did to two clean ones.
func TestATrialRefusalIsHeldNotRejected(t *testing.T) {
	in := gateInput(cleanProposal(), autogate.Caps{PerDay: 2, PerWeek: 5, MaxOpen: 10}, false)
	in.Grant = standingGrant("devops-go", autoNow.Add(-time.Hour), true, autoNow)
	d := autogate.Decide(in)
	if d.Allowed || !onlyTemporary(d.Blockers) {
		t.Fatalf("trial refusal: allowed=%v temporary=%v (%s)", d.Allowed, onlyTemporary(d.Blockers), d.Why())
	}
}

func TestStandingGrantWithATrialWaits(t *testing.T) {
	in := gateInput(cleanProposal(), autogate.Caps{PerDay: 2, PerWeek: 5, MaxOpen: 10}, false)
	in.Grant = standingGrant("devops-go", autoNow.Add(-time.Hour), true, autoNow)
	d := autogate.Decide(in)
	if d.Allowed || !d.WouldAllow {
		t.Fatalf("a trial grant acted, or the trial recorded nothing: %+v", d)
	}
}

// The coupling that matters most here: a cap refusal must read as temporary,
// or a proposal held for a day would be rejected for good.
func TestCapRefusalsAreHeldNotRejected(t *testing.T) {
	cases := map[string]autogate.Caps{
		"daily":  {PerDay: 2, PerWeek: 5, MaxOpen: 10, UsedToday: 2},
		"weekly": {PerDay: 2, PerWeek: 5, MaxOpen: 10, UsedThisWeek: 5},
		"open":   {PerDay: 2, PerWeek: 5, MaxOpen: 10, OpenNow: 10},
	}
	for name, caps := range cases {
		d := autogate.Decide(gateInput(cleanProposal(), caps, false))
		if d.Allowed || !onlyTemporary(d.Blockers) {
			t.Errorf("%s cap: allowed=%v, temporary=%v (%s)", name, d.Allowed, onlyTemporary(d.Blockers), d.Why())
		}
	}
	d := autogate.Decide(gateInput(cleanProposal(), autogate.Caps{PerDay: 2, PerWeek: 5, MaxOpen: 10}, true))
	if d.Allowed || !onlyTemporary(d.Blockers) {
		t.Errorf("per-repo hold: %s", d.Why())
	}
}

func TestALastingRefusalIsNotHeld(t *testing.T) {
	c := cleanProposal()
	c.SoftPenalties = []string{"no CONTRIBUTING.md"}
	// A cap is also hit, but the penalty will not clear by waiting.
	d := autogate.Decide(gateInput(c, autogate.Caps{PerDay: 2, PerWeek: 5, MaxOpen: 10, UsedToday: 2}, false))
	if onlyTemporary(d.Blockers) {
		t.Fatalf("held a proposal that waiting cannot fix: %s", d.Why())
	}
}

func TestCountAutoCountsFromHistory(t *testing.T) {
	all := []*model.Candidate{
		autoCand("a/a", 1, model.StatusPROpen, 2*time.Hour),          // today, week, open
		autoCand("a/b", 2, model.StatusMerged, 3*time.Hour),          // today, week, not open
		autoCand("a/c", 3, model.StatusAutoApproved, 3*24*time.Hour), // week, open
		autoCand("a/d", 4, model.StatusClosed, 9*24*time.Hour),       // none
		{Repo: "a/e", Issue: 5, Status: model.StatusPROpen, // human-approved: none
			History: []model.HistoryEntry{{At: ago(time.Hour), From: "proposed", To: "approved"}}},
	}
	got := countAuto(all, autoNow)
	if got != (autoCounts{today: 2, week: 3, open: 2}) {
		t.Fatalf("counts = %+v", got)
	}
}

func TestRepoRampOneAtATimeUntilAMerge(t *testing.T) {
	open := autoCand("x/y", 1, model.StatusPROpen, time.Hour)
	if !repoHoldsAutonomy([]*model.Candidate{open}, "x/y") {
		t.Error("a second autonomous PR was allowed beside an unmerged first")
	}
	closed := autoCand("x/y", 1, model.StatusClosed, time.Hour)
	if !repoHoldsAutonomy([]*model.Candidate{closed}, "x/y") {
		t.Error("autonomy continued on a repo that closed our PR unmerged")
	}
	merged := autoCand("x/y", 2, model.StatusMerged, 48*time.Hour)
	if repoHoldsAutonomy([]*model.Candidate{open, merged}, "x/y") {
		t.Error("still held after a merge on the repo")
	}
	abandoned := autoCand("x/y", 3, model.StatusAbandoned, time.Hour)
	if repoHoldsAutonomy([]*model.Candidate{abandoned}, "x/y") {
		t.Error("an approval that never became a PR held the repo")
	}
	if repoHoldsAutonomy([]*model.Candidate{open}, "other/repo") {
		t.Error("held a different repository")
	}
}
