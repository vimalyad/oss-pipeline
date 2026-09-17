package score

import (
	"strings"
	"testing"
	"time"

	"github.com/vimalyad/osspipeline/internal/model"
	"github.com/vimalyad/osspipeline/internal/policy"
)

func cfg() *policy.Config {
	c := &policy.Config{TouchedByOtherAccounts: map[string]bool{}}
	c.Policy.Scoring = policy.Scoring{
		RequireConvergedThread: true, RequireMaintainerAcceptance: true,
		RequireApproachOrCriteria: true, RequireTests: true,
		RejectDocsTypoOnly: true, FirstTimeWindowDays: 90,
		StaleIssuePenaltyYears: 2,
	}
	c.Policy.Staleness.ClaimHonouredDays = 30
	c.Policy.AcceptanceLabels = []string{"good first issue", "help wanted", "bug"}
	c.Policy.UntriagedLabels = []string{"triage/pending", "needs-triage"}
	return c
}

// good is a candidate that clears every bar, so each test can break one thing.
func good() *model.Candidate {
	return &model.Candidate{
		Repo: "acme/widget", Issue: 1, Title: "Panic when the config is empty",
		Status: model.StatusDiscovered, Contest: model.ContestNoPR,
		Labels: []string{"bug"}, Comments: 5,
		IssueCreatedAt: time.Now().AddDate(0, -1, 0).Format(time.RFC3339),
		Facts: &model.RepoFacts{
			Repo: "acme/widget", HasContributing: true, HasTests: true,
			MergedFirstTimePR90d: true, PrimaryLanguage: "Go",
		},
		Brief: &model.Brief{MaintainerDesiredApproach: "guard the nil case",
			ApproachAuthorAssociation: "MEMBER"},
	}
}

func TestGoodCandidatePasses(t *testing.T) {
	r := Score(good(), cfg(), nil, Deps{})
	if !r.OK {
		t.Fatalf("should pass, failed with: %v", r.Fails)
	}
}

func TestEachBarRejectsIndependently(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		breakIt    func(c *model.Candidate)
	}{
		{"contested by an active PR", "contest class",
			func(c *model.Candidate) { c.Contest = model.ContestActivePR }},
		{"no test suite", "no test suite",
			func(c *model.Candidate) { c.Facts.HasTests = false }},
		{"unreceptive to newcomers", "unreceptive",
			func(c *model.Candidate) { c.Facts.MergedFirstTimePR90d = false }},
		{"repo bans AI PRs", "bans AI-assisted",
			func(c *model.Candidate) { c.Facts.BansAIPRs = true }},
		{"untriaged label vetoes acceptance", "untriaged label",
			func(c *model.Candidate) {
				c.Labels = []string{"triage/pending"}
				c.Brief.MaintainerDesiredApproach = ""
			}},
		{"thread has not converged", "not converged",
			func(c *model.Candidate) { c.Brief.OpenQuestions = []string{"which API?"} }},
		{"someone claimed it", "respect the claim",
			func(c *model.Candidate) {
				c.Brief.ClaimedBy = "someone"
				c.Brief.ClaimedAt = time.Now().AddDate(0, 0, -3).Format(time.RFC3339)
			}},
		{"docs typo only", "docs/typo-only",
			func(c *model.Candidate) { c.Title = "Fix typo in the README" }},
		{"required label missing", "only for issues labelled",
			func(c *model.Candidate) {
				c.Facts.RequiredIssueLabels = []string{"help wanted"}
			}},
		{"no brief at all", "no brief",
			func(c *model.Candidate) { c.Brief = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := good()
			tc.breakIt(c)
			r := Score(c, cfg(), nil, Deps{})
			if r.OK {
				t.Fatal("should have been rejected")
			}
			if !strings.Contains(strings.Join(r.Fails, " | "), tc.want) {
				t.Errorf("want a failure mentioning %q, got %v", tc.want, r.Fails)
			}
		})
	}
}

// A claim that has gone quiet should stop blocking, or a stalled claim holds
// an issue forever.
func TestStaleClaimNoLongerBlocks(t *testing.T) {
	c := good()
	c.Brief.ClaimedBy = "someone"
	c.Brief.ClaimedAt = time.Now().AddDate(0, 0, -120).Format(time.RFC3339)
	if r := Score(c, cfg(), nil, Deps{}); !r.OK {
		t.Fatalf("a 120-day-old claim should have aged out: %v", r.Fails)
	}
}

// Penalties rank lower; they must never reject.
func TestPenaltiesDoNotReject(t *testing.T) {
	c := good()
	c.Facts.HasContributing = false
	c.IssueCreatedAt = time.Now().AddDate(-4, 0, 0).Format(time.RFC3339)
	r := Score(c, cfg(), nil, Deps{})
	if !r.OK {
		t.Fatalf("penalties must not reject: %v", r.Fails)
	}
	if len(r.Penalties) != 2 {
		t.Errorf("want both penalties, got %v", r.Penalties)
	}
}

// Blockers need one human action; they also must not reject, or the candidate
// disappears instead of appearing as something the user can unblock.
func TestBlockersDoNotReject(t *testing.T) {
	c := good()
	c.Facts.RequiresCLA = true
	r := Score(c, cfg(), nil, Deps{})
	if !r.OK {
		t.Fatalf("a CLA is a blocker, not a rejection: %v", r.Fails)
	}
	if len(r.Blockers) != 1 || !strings.Contains(r.Blockers[0], "CLA") {
		t.Fatalf("blockers = %v", r.Blockers)
	}
}

func TestCrossAccountRepoIsRejected(t *testing.T) {
	c := cfg()
	c.TouchedByOtherAccounts["acme/widget"] = true
	r := Score(good(), c, []string{"otheracct"}, Deps{})
	if r.OK {
		t.Fatal("a repo another of the user's accounts touched must be refused")
	}
	if !strings.Contains(r.Fails[0], "otheracct") {
		t.Errorf("the reason should name the account: %v", r.Fails)
	}
}

// Facts embedded in the candidate are preferred, but a cached fetch is the
// fallback -- without it every candidate lacking inline facts is rejected.
func TestFactsFallBackToTheCache(t *testing.T) {
	c := good()
	c.Facts = nil
	called := ""
	d := Deps{LoadFacts: func(repo string) *model.RepoFacts {
		called = repo
		return &model.RepoFacts{HasTests: true, MergedFirstTimePR90d: true,
			HasContributing: true}
	}}
	if r := Score(c, cfg(), nil, d); !r.OK {
		t.Fatalf("cached facts should satisfy the bars: %v", r.Fails)
	}
	if called != "acme/widget" {
		t.Errorf("LoadFacts called with %q", called)
	}
}

func TestPyReprMatchesPythonQuoting(t *testing.T) {
	for in, want := range map[string]string{
		"plain":     `'plain'`,
		"it's":      `"it's"`,
		`say "hi"`:  `'say "hi"'`,
		"line\nbrk": `'line\nbrk'`,
		"tab\there": `'tab\there'`,
	} {
		if got := pyRepr(in); got != want {
			t.Errorf("pyRepr(%q) = %s, want %s", in, got, want)
		}
	}
}
