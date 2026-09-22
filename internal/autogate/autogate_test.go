package autogate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vimalyad/osspipeline/internal/model"
	"github.com/vimalyad/osspipeline/internal/profile"
)

var now = time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)

// passing returns an Input where every gate is satisfied, so each test can
// break exactly one thing and prove that one thing is load-bearing.
func passing() Input {
	return Input{
		Now:  now,
		Want: profile.AutonomyOpenPRs,
		Candidate: &model.Candidate{
			Repo: "helm/helm", Issue: 13284, Status: model.StatusProposed,
			Contest: model.ContestNoPR,
			Brief:   &model.Brief{MaintainerDesiredApproach: "follow the symlink and skip it"},
			Facts:   &model.RepoFacts{Repo: "helm/helm", HasTests: true},
		},
		Domain: profile.Domain{ID: "devops-go", Autonomy: profile.AutonomyOpenPRs},
		Grant: Grant{
			Domain: "devops-go", Level: profile.AutonomyOpenPRs,
			GrantedAt: now.Add(-30 * 24 * time.Hour), ExpiresAt: now.Add(60 * 24 * time.Hour),
			Countersigned: true, CountersignedAt: now.Add(-30 * 24 * time.Hour),
			TrialStartedAt: now.Add(-30 * 24 * time.Hour),
		},
		Record:    DomainRecord{Merged: 4, Closed: 2},
		Probation: Probation{MinDecided: 6, MinRate: 0.5},
		Caps:      Caps{PerDay: 1, MaxOpen: 2, UsedToday: 0, OpenNow: 0},
	}
}

func TestTheHappyPathIsActuallyReachable(t *testing.T) {
	// Without this, every other test here could pass while autonomy is
	// permanently off for an unrelated reason.
	d := Decide(passing())
	if !d.Allowed {
		t.Fatalf("a fully satisfied input was refused: %s", d.Why())
	}
	if !d.WouldAllow || d.InTrial {
		t.Errorf("d = %+v", d)
	}
}

// TestEveryGateIsLoadBearing breaks one thing at a time. A guardrail that can
// be removed without a test failing is not a guardrail.
func TestEveryGateIsLoadBearing(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Input)
		want   string
	}{
		{"halt", func(in *Input) { in.Halted, in.HaltReason = true, "user pressed stop" }, "HALT"},
		{"no grant", func(in *Input) { in.Grant.Level = "" }, "no autonomy grant"},
		{"grant not countersigned", func(in *Input) { in.Grant.Countersigned = false }, "never countersigned"},
		{"grant expired", func(in *Input) { in.Grant.ExpiresAt = now.Add(-time.Hour) }, "expired on"},
		{"grant has no expiry", func(in *Input) { in.Grant.ExpiresAt = time.Time{} }, "no expiry"},
		{"grant below the action", func(in *Input) { in.Grant.Level = profile.AutonomyFix }, "below"},
		{"profile below the action", func(in *Input) { in.Domain.Autonomy = profile.AutonomyReply }, "in the profile"},
		{"too few decided PRs", func(in *Input) { in.Record = DomainRecord{Merged: 2, Closed: 1} }, "needs 6"},
		{"merge rate too low", func(in *Input) { in.Record = DomainRecord{Merged: 2, Closed: 5} }, "merge rate"},
		{"daily cap", func(in *Input) { in.Caps.UsedToday = 1 }, "1/1 today"},
		{"open cap", func(in *Input) { in.Caps.OpenNow = 2 }, "2/2 open"},
		{"novelty", func(in *Input) { in.RepoHasAutoPR = true }, "already has an autonomous PR"},
		{"still in trial", func(in *Input) { in.Grant.TrialStartedAt = now.Add(-2 * 24 * time.Hour) }, "silent trial"},

		// The quality bar, stricter than the manual one.
		{"not proposed", func(in *Input) { in.Candidate.Status = model.StatusScored }, "not proposed"},
		{"scoring failures", func(in *Input) { in.Candidate.ScoreFailures = []string{"no tests"} }, "scoring failure"},
		{"soft penalty", func(in *Input) { in.Candidate.SoftPenalties = []string{"issue is 3 years old"} }, "soft penalty"},
		{"blocker", func(in *Input) { in.Candidate.Blockers = []string{"sign the CLA"} }, "blocker needing a human"},
		{"someone else's PR", func(in *Input) { in.Candidate.Contest = model.ContestStalePR }, "not no_pr"},
		{"no stated approach", func(in *Input) { in.Candidate.Brief = nil }, "stated an approach"},
		{"repo bans AI PRs", func(in *Input) { in.Candidate.Facts.BansAIPRs = true }, "does not accept AI"},
		{"repo requires disclosure", func(in *Input) { in.Candidate.Facts.RequiresAIDisclosure = true }, "requires AI disclosure"},
		{"repo requires a CLA", func(in *Input) { in.Candidate.Facts.RequiresCLA = true }, "requires a CLA"},
		{"no repo facts", func(in *Input) { in.Candidate.Facts = nil }, "no repo facts"},
		{"no candidate", func(in *Input) { in.Candidate = nil }, "no candidate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := passing()
			tt.mutate(&in)
			d := Decide(in)
			if d.Allowed {
				t.Fatalf("allowed despite %s", tt.name)
			}
			if !strings.Contains(d.Why(), tt.want) {
				t.Errorf("why = %q, want it to mention %q", d.Why(), tt.want)
			}
		})
	}
}

// TestHaltShortCircuits: nothing may be reported alongside HALT, because a
// list of other blockers invites fixing them and trying again.
func TestHaltShortCircuits(t *testing.T) {
	in := passing()
	in.Halted, in.HaltReason = true, "stopped from the phone"
	in.Caps.UsedToday = 99
	in.Candidate.SoftPenalties = []string{"whatever"}

	d := Decide(in)
	if len(d.Blockers) != 1 || !strings.Contains(d.Blockers[0], "HALT") {
		t.Fatalf("blockers = %v; HALT must be the only one reported", d.Blockers)
	}
}

// TestBlockersAccumulate: reporting one at a time turns a single round of
// fixes into five.
func TestBlockersAccumulate(t *testing.T) {
	in := passing()
	in.Caps.UsedToday = 1
	in.RepoHasAutoPR = true
	in.Candidate.SoftPenalties = []string{"stale issue"}

	d := Decide(in)
	if len(d.Blockers) < 3 {
		t.Fatalf("blockers = %v, want all three", d.Blockers)
	}
	// Deterministic order, so a digest does not churn between runs.
	for i := 1; i < len(d.Blockers); i++ {
		if d.Blockers[i] < d.Blockers[i-1] {
			t.Fatalf("blockers are not sorted: %v", d.Blockers)
		}
	}
}

// TestTheTrialRecordsWhatItWouldHaveDone: the trial's entire value is in the
// counterfactuals, and one that only logs the times it would have acted cannot
// show whether the gates are too tight.
func TestTheTrialRecordsWhatItWouldHaveDone(t *testing.T) {
	in := passing()
	in.Grant.TrialStartedAt = now.Add(-2 * 24 * time.Hour)

	d := Decide(in)
	if d.Allowed {
		t.Fatal("acted during the silent trial")
	}
	if !d.WouldAllow {
		t.Fatal("a decision blocked only by the trial must record that it would have acted")
	}
	cf := Record(in, d)
	if !cf.WouldAllow || cf.Slug != "helm__helm__13284" || cf.Domain != "devops-go" {
		t.Fatalf("counterfactual = %+v", cf)
	}

	// A refusal is recorded too.
	in2 := passing()
	in2.Caps.UsedToday = 1
	cf2 := Record(in2, Decide(in2))
	if cf2.WouldAllow || !strings.Contains(cf2.Why, "cap") {
		t.Fatalf("refusal counterfactual = %+v", cf2)
	}
}

func TestAMissingTrialStartIsTreatedAsInTrial(t *testing.T) {
	// A zero time must not read as "the trial finished long ago".
	in := passing()
	in.Grant.TrialStartedAt = time.Time{}
	if Decide(in).Allowed {
		t.Fatal("a grant with no recorded trial start acted immediately")
	}
}

func TestEffectiveDegradesSilently(t *testing.T) {
	tests := []struct {
		name string
		g    Grant
		want profile.Autonomy
	}{
		{"valid", Grant{Level: profile.AutonomyFix, Countersigned: true,
			ExpiresAt: now.Add(time.Hour)}, profile.AutonomyFix},
		{"expired", Grant{Level: profile.AutonomyOpenPRs, Countersigned: true,
			ExpiresAt: now.Add(-time.Hour)}, profile.AutonomyOff},
		{"expires exactly now", Grant{Level: profile.AutonomyOpenPRs, Countersigned: true,
			ExpiresAt: now}, profile.AutonomyOff},
		{"not countersigned", Grant{Level: profile.AutonomyOpenPRs,
			ExpiresAt: now.Add(time.Hour)}, profile.AutonomyOff},
		{"no expiry", Grant{Level: profile.AutonomyOpenPRs, Countersigned: true}, profile.AutonomyOff},
		{"zero grant", Grant{}, profile.AutonomyOff},
		{"nonsense level", Grant{Level: "yes-please", Countersigned: true,
			ExpiresAt: now.Add(time.Hour)}, profile.AutonomyOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.g.Effective(now); got != tt.want {
				t.Errorf("= %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFixSitsBelowReply: pushing a lint fix is recoverable; posting under the
// user's name is not. A grant at `fix` must not authorise a reply.
func TestFixSitsBelowReply(t *testing.T) {
	in := passing()
	in.Grant.Level = profile.AutonomyFix
	in.Domain.Autonomy = profile.AutonomyFix

	in.Want = profile.AutonomyFix
	if d := Decide(in); !d.Allowed {
		t.Fatalf("a fix grant refused a fix: %s", d.Why())
	}
	for _, want := range []profile.Autonomy{profile.AutonomyReply, profile.AutonomyOpenPRs} {
		in.Want = want
		if d := Decide(in); d.Allowed {
			t.Errorf("a %q grant authorised %q", profile.AutonomyFix, want)
		}
	}
}

func TestApproveRefusesWhenTheDecisionDid(t *testing.T) {
	c := &model.Candidate{Repo: "a/b", Issue: 1, Status: model.StatusProposed}
	d := Decision{Allowed: false, Blockers: []string{"cap reached"}}
	if err := Approve(c, d, now); err == nil {
		t.Fatal("approved on a refused decision")
	}
	if c.Status != model.StatusProposed {
		t.Fatalf("status changed to %s", c.Status)
	}
}

func TestApproveWritesTheLabelledStatus(t *testing.T) {
	c := &model.Candidate{Repo: "a/b", Issue: 1, Status: model.StatusProposed}
	if err := Approve(c, Decision{Allowed: true, Level: profile.AutonomyOpenPRs}, now); err != nil {
		t.Fatal(err)
	}
	if c.Status != model.StatusAutoApproved {
		t.Fatalf("status = %s, want auto_approved", c.Status)
	}
	if len(c.History) == 0 || !strings.Contains(c.History[len(c.History)-1].Note, "autonomous") {
		t.Error("the audit entry does not say the approval was autonomous")
	}
}

// TestOnlyAutogateWritesAutoApproved is the claim that makes every pull
// request attributable. It reads the source rather than trusting a convention,
// because a convention is exactly what fails silently.
//
// It looks for *writes*, not mentions: passing the constant to
// model.Transition, or assigning it to a field. Reading it -- a switch that
// decides which queued candidates `exclude` should drop, say -- is ordinary
// and must stay allowed, or the rule would push other packages into comparing
// raw strings, which is worse in every way.
func TestOnlyAutogateWritesAutoApproved(t *testing.T) {
	fset := token.NewFileSet()
	var offenders []string

	isAutoApproved := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "StatusAutoApproved" {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && id.Name == "model"
	}
	note := func(path string, pos token.Pos, what string) {
		offenders = append(offenders,
			fmt.Sprintf("%s:%d (%s)", path, fset.Position(pos).Line, what))
	}

	err := filepath.Walk(filepath.Join("..", ".."), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		slash := filepath.ToSlash(path)
		// autogate is the one place allowed to write it; model declares the
		// constant and the table without ever assigning it to a candidate.
		if strings.Contains(slash, "internal/autogate/") || strings.Contains(slash, "internal/model/") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch t := n.(type) {
			case *ast.CallExpr:
				sel, ok := t.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name != "Transition" && sel.Sel.Name != "Reopen" {
					return true
				}
				for _, a := range t.Args {
					if isAutoApproved(a) {
						note(path, a.Pos(), "transition target")
					}
				}
			case *ast.AssignStmt:
				for _, r := range t.Rhs {
					if isAutoApproved(r) {
						note(path, r.Pos(), "assignment")
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("model.StatusAutoApproved is written outside internal/autogate:\n  %s\n"+
			"every PR must be attributable to a human approval or to autogate",
			strings.Join(offenders, "\n  "))
	}
}
