// Package autogate decides whether the pipeline may act without asking.
//
// It is the only package permitted to write StatusAutoApproved, which a source
// test enforces, so every pull request is attributable to a human approval or
// to this file from the audit log alone.
//
// The guardrails are all required and all independent. That is deliberate:
// each one is cheap, and a single check -- however carefully written -- is a
// single thing to get wrong in a loop that speaks publicly under someone's
// real name. A refusal always says which gate stopped it, because a silent
// "no" is indistinguishable from a broken pipeline.
package autogate

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vimalyad/osspipeline/internal/model"
	"github.com/vimalyad/osspipeline/internal/profile"
)

// Grant is a countersigned authorisation for one domain at one level.
//
// Countersigning is two distinct confirmations rather than typing a label
// back, because every decision here is made on a phone. Expiry is not a
// reminder to renew: the grant simply stops applying, and the pipeline returns
// to asking. An authorisation that outlives the attention that produced it is
// not an authorisation.
type Grant struct {
	Domain          string           `json:"domain"`
	Level           profile.Autonomy `json:"level"`
	GrantedAt       time.Time        `json:"granted_at"`
	ExpiresAt       time.Time        `json:"expires_at"`
	Countersigned   bool             `json:"countersigned"`
	CountersignedAt time.Time        `json:"countersigned_at"`
	// TrialStartedAt begins the silent period in which decisions are recorded
	// but never acted on.
	TrialStartedAt time.Time `json:"trial_started_at"`
}

// DefaultTTL is how long a grant lasts before it degrades to off.
const DefaultTTL = 90 * 24 * time.Hour

// TrialDays is the silent period after a grant, during which every decision is
// written as a counterfactual and nothing is acted on. Its purpose is to make
// the first real autonomous action the second thing that happens, not the
// first.
const TrialDays = 14

// Effective is the level actually in force, which degrades silently rather
// than erroring.
func (g Grant) Effective(now time.Time) profile.Autonomy {
	switch {
	case g.Level == "" || !g.Level.Valid():
		return profile.AutonomyOff
	case !g.Countersigned:
		return profile.AutonomyOff
	case g.ExpiresAt.IsZero() || !now.Before(g.ExpiresAt):
		return profile.AutonomyOff
	}
	return g.Level
}

// InTrial reports whether the silent period is still running.
func (g Grant) InTrial(now time.Time) bool {
	if g.TrialStartedAt.IsZero() {
		return true // no trial recorded is not the same as a trial completed.
	}
	return now.Before(g.TrialStartedAt.Add(TrialDays * 24 * time.Hour))
}

// DomainRecord is the merge record for one domain.
//
// Per domain, not global: a strong record in one area says nothing about
// another, and a global figure would let the pipeline's success at Python
// documentation unlock autonomous work on a Go release tool.
type DomainRecord struct {
	Merged int
	Closed int
}

func (d DomainRecord) Decided() int { return d.Merged + d.Closed }

func (d DomainRecord) Rate() float64 {
	if d.Decided() == 0 {
		return 0
	}
	return float64(d.Merged) / float64(d.Decided())
}

// Caps are the strict subset of the overall budget that autonomous work gets,
// so a bug here cannot consume the whole allowance.
type Caps struct {
	PerDay    int
	MaxOpen   int
	UsedToday int
	OpenNow   int
}

// Probation is the per-domain bar, reusing the tier-unlock thresholds.
type Probation struct {
	MinDecided int
	MinRate    float64
}

// Input is everything a decision depends on, gathered by the caller so this
// package performs no I/O and every refusal is reproducible from a struct.
type Input struct {
	Now  time.Time
	Want profile.Autonomy

	Halted     bool
	HaltReason string

	Candidate *model.Candidate
	Domain    profile.Domain
	Grant     Grant
	Record    DomainRecord
	Probation Probation
	Caps      Caps

	// RepoHasAutoPR is true when this repository has already received an
	// autonomous pull request.
	RepoHasAutoPR bool
}

// Decision is the answer and the reasoning for it.
type Decision struct {
	// Allowed is true only when the action may actually happen now.
	Allowed bool
	// WouldAllow is true when every gate passed except the silent trial. It is
	// what a counterfactual records: the trial's value is entirely in these.
	WouldAllow bool
	InTrial    bool
	// Blockers are every gate that refused, not just the first, so one round
	// of fixes can clear them all.
	Blockers []string
	Level    profile.Autonomy
}

func (d Decision) Why() string {
	if d.Allowed {
		return "all autonomy gates passed"
	}
	if len(d.Blockers) == 0 {
		return "refused"
	}
	return strings.Join(d.Blockers, "; ")
}

// Decide answers whether the wanted action may proceed.
//
// HALT short-circuits; everything else accumulates, because a digest that
// reports one blocker at a time turns a single fix into five rounds.
func Decide(in Input) Decision {
	d := Decision{Level: in.Grant.Effective(in.Now)}

	// HALT is absolute and is checked before anything else can have an
	// opinion. Nothing below may override it.
	if in.Halted {
		d.Blockers = []string{"HALT is set: " + orElse(in.HaltReason, "no reason recorded")}
		return d
	}

	if in.Want == "" || !in.Want.Valid() {
		d.Blockers = append(d.Blockers, fmt.Sprintf("unknown action level %q", in.Want))
		return d
	}
	if !d.Level.AtLeast(in.Want) {
		d.Blockers = append(d.Blockers, grantReason(in))
	}
	if in.Domain.Autonomy == "" || !in.Domain.Autonomy.AtLeast(in.Want) {
		// The profile and the grant must agree. The profile says what the
		// user wants in principle; the grant says what they have actually
		// authorised, and it expires.
		d.Blockers = append(d.Blockers, fmt.Sprintf(
			"domain %q is set to autonomy %q in the profile, below %q",
			in.Domain.ID, orElse(string(in.Domain.Autonomy), "off"), in.Want))
	}

	if in.Record.Decided() < in.Probation.MinDecided {
		d.Blockers = append(d.Blockers, fmt.Sprintf(
			"domain %q has %d decided PR(s), needs %d",
			in.Domain.ID, in.Record.Decided(), in.Probation.MinDecided))
	} else if in.Record.Rate() < in.Probation.MinRate {
		d.Blockers = append(d.Blockers, fmt.Sprintf(
			"domain %q merge rate %.0f%%, needs %.0f%%",
			in.Domain.ID, in.Record.Rate()*100, in.Probation.MinRate*100))
	}

	if in.Caps.UsedToday >= in.Caps.PerDay {
		d.Blockers = append(d.Blockers, fmt.Sprintf(
			"autonomous cap reached: %d/%d today", in.Caps.UsedToday, in.Caps.PerDay))
	}
	if in.Caps.OpenNow >= in.Caps.MaxOpen {
		d.Blockers = append(d.Blockers, fmt.Sprintf(
			"autonomous cap reached: %d/%d open", in.Caps.OpenNow, in.Caps.MaxOpen))
	}
	if in.RepoHasAutoPR {
		// Novelty. Two unattended pull requests on one repository is how a
		// contributor becomes a nuisance, and the second one arrives before
		// anyone has reacted to the first.
		d.Blockers = append(d.Blockers, "this repository already has an autonomous PR")
	}

	d.Blockers = append(d.Blockers, qualityBlockers(in.Candidate)...)

	d.InTrial = in.Grant.InTrial(in.Now)
	if len(d.Blockers) == 0 {
		// Every gate passed. Whether it happens now depends only on the trial.
		d.WouldAllow = true
		d.Allowed = !d.InTrial
		if d.InTrial {
			d.Blockers = append(d.Blockers, fmt.Sprintf(
				"silent trial: %d days from %s", TrialDays, in.Grant.TrialStartedAt.Format("2006-01-02")))
		}
	}
	sort.Strings(d.Blockers)
	return d
}

// qualityBlockers is the bar that is deliberately stricter than the manual one.
//
// Each entry is a judgement a human makes without noticing and a loop cannot
// make at all.
func qualityBlockers(c *model.Candidate) []string {
	if c == nil {
		return []string{"no candidate"}
	}
	var out []string

	if c.Status != model.StatusProposed {
		out = append(out, fmt.Sprintf("candidate is %q, not proposed", c.Status))
	}
	if len(c.ScoreFailures) > 0 {
		out = append(out, fmt.Sprintf("%d scoring failure(s)", len(c.ScoreFailures)))
	}
	// A soft penalty ranks a candidate below cleaner ones when a person is
	// choosing. With nobody choosing, "ranked lower" means nothing, so the
	// only safe reading is to require a clean one.
	if len(c.SoftPenalties) > 0 {
		out = append(out, fmt.Sprintf("%d soft penalty(ies): %s",
			len(c.SoftPenalties), first(c.SoftPenalties)))
	}
	if len(c.Blockers) > 0 {
		out = append(out, "has a blocker needing a human: "+first(c.Blockers))
	}
	// Taking over someone's abandoned pull request is a social call, not a
	// technical one, and it is not one to make unattended.
	if c.Contest != model.ContestNoPR {
		out = append(out, fmt.Sprintf("contest is %q, not no_pr", orElse(string(c.Contest), "unknown")))
	}
	if c.Brief == nil || strings.TrimSpace(c.Brief.MaintainerDesiredApproach) == "" {
		out = append(out, "no maintainer has stated an approach")
	}
	if c.Facts == nil {
		out = append(out, "no repo facts")
	} else {
		if c.Facts.BansAIPRs {
			out = append(out, "the repository does not accept AI-assisted PRs")
		}
		if c.Facts.RequiresAIDisclosure {
			// Disclosure asserts that a human reviewed the change. On the
			// autonomous path nobody did, so the disclosure would be false.
			out = append(out, "the repository requires AI disclosure, which would assert a review nobody performed")
		}
		if c.Facts.RequiresCLA {
			out = append(out, "the repository requires a CLA, which only a person can sign")
		}
	}
	return out
}

func grantReason(in Input) string {
	g := in.Grant
	switch {
	case g.Level == "" || !g.Level.Valid():
		return fmt.Sprintf("no autonomy grant for domain %q", in.Domain.ID)
	case !g.Countersigned:
		return fmt.Sprintf("the grant for %q was never countersigned", in.Domain.ID)
	case g.ExpiresAt.IsZero():
		return fmt.Sprintf("the grant for %q has no expiry and is treated as expired", in.Domain.ID)
	case !in.Now.Before(g.ExpiresAt):
		return fmt.Sprintf("the grant for %q expired on %s",
			in.Domain.ID, g.ExpiresAt.Format("2006-01-02"))
	default:
		return fmt.Sprintf("grant for %q is %q, below %q", in.Domain.ID, g.Level, in.Want)
	}
}

// Approve records an autonomous approval.
//
// This is the only place StatusAutoApproved is written, and
// TestOnlyAutogateWritesAutoApproved enforces that against the source rather
// than against a convention.
func Approve(c *model.Candidate, d Decision, now time.Time) error {
	if !d.Allowed {
		return fmt.Errorf("autogate: refusing to approve %s: %s", c.Slug(), d.Why())
	}
	return model.Transition(c, model.StatusAutoApproved,
		fmt.Sprintf("autonomous approval at level %q on %s", d.Level, now.Format("2006-01-02")))
}

// Counterfactual is what the silent trial writes instead of acting.
type Counterfactual struct {
	At         string `json:"at"`
	Slug       string `json:"slug"`
	Domain     string `json:"domain"`
	Want       string `json:"want"`
	WouldAllow bool   `json:"would_allow"`
	Why        string `json:"why"`
}

// Record turns a decision into the trial's audit line. Written for refusals
// too: a trial that only logs the times it would have acted cannot show
// whether the gates are too tight.
func Record(in Input, d Decision) Counterfactual {
	slug := ""
	if in.Candidate != nil {
		slug = in.Candidate.Slug()
	}
	return Counterfactual{
		At: in.Now.UTC().Format(time.RFC3339), Slug: slug, Domain: in.Domain.ID,
		Want: string(in.Want), WouldAllow: d.WouldAllow, Why: d.Why(),
	}
}

func first(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

func orElse(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
