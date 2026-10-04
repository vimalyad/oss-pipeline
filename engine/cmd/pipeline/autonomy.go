package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/vimalyad/oss-pipeline/engine/internal/autogate"
	"github.com/vimalyad/oss-pipeline/engine/internal/halt"
	"github.com/vimalyad/oss-pipeline/engine/internal/model"
	"github.com/vimalyad/oss-pipeline/engine/internal/policy"
	"github.com/vimalyad/oss-pipeline/engine/internal/profile"
)

// autoApproveCmd puts every waiting proposal through internal/autogate.
//
// This is the operator's choice to take themselves out of the loop: nobody
// approves proposals by hand. What decides instead is autogate's full set of
// gates -- HALT, the autonomous caps, one unmerged autonomous pull request per
// repository, and a quality bar stricter than the manual one -- and every
// decision lands in the audit log under "autogate", so each pull request is
// still attributable to the rule that let it through.
//
// A refusal is one of two kinds. A cap or the per-repository limit is true
// today only, so the proposal stays queued for the next run. Anything else
// -- a soft penalty, someone else's pull request, no stated approach, a
// repository that bans automated contributions -- will not change by waiting,
// and with nobody to look at it the proposal would sit in the queue forever.
// Those are rejected, with every gate that refused written into the reason.
func autoApproveCmd(root string, args []string) int {
	execute := false
	for _, a := range args {
		switch a {
		case "--execute":
			execute = true
		default:
			fmt.Fprintf(os.Stderr, "unknown argument %q\n", a)
			return 2
		}
	}
	cfg, err := policy.Load(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	auto := cfg.Policy.Autonomy
	if !auto.Standing() {
		fmt.Println("autonomy is off (policy.yaml autonomy.mode); proposals wait for a person")
		return 0
	}
	since, err := time.Parse("2006-01-02", auto.Since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: autonomy.since %q is not a YYYY-MM-DD date\n", auto.Since)
		return 1
	}
	prof, err := profile.Load(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	st, ok := mustStore(root)
	if !ok {
		return 1
	}
	log := openAudit(root)
	haltReason, halted := halt.New(root).Active()

	all, _ := st.All()
	queue := st.ByStatus(model.StatusProposed)
	// Oldest proposal first: it has waited longest and its issue is the one
	// going stale.
	sort.SliceStable(queue, func(i, j int) bool { return queue[i].Slug() < queue[j].Slug() })
	sort.SliceStable(queue, func(i, j int) bool { return proposedAt(queue[i]) < proposedAt(queue[j]) })

	now := time.Now()
	caps := cfg.Policy.Caps
	counts := countAuto(all, now)
	approved, rejected, held, problems := 0, 0, 0, 0
	// Repositories a dry run has approved on. A preview must make the same
	// later decisions the real run would, but it may not write an approval --
	// only autogate.Approve does that, and its source test holds it to that.
	previewRepos := map[string]bool{}

	for _, c := range queue {
		lang, topics := "", []string(nil)
		if c.Facts != nil {
			lang, topics = c.Facts.PrimaryLanguage, c.Facts.Topics
		}
		domain, matched := prof.ForRepo(c.Repo, lang, topics)

		in := autogate.Input{
			Now:        now,
			Want:       profile.AutonomyOpenPRs,
			Halted:     halted,
			HaltReason: haltReason,
			Candidate:  c,
			Domain:     domain,
			Grant:      standingGrant(domain.ID, since, auto.Trial, now),
			Record:     domainRecord(all, prof, domain.ID),
			Probation: autogate.Probation{
				MinDecided: auto.ProbationMinDecided,
				MinRate:    auto.ProbationMinRate,
			},
			Caps: autogate.Caps{
				PerDay: caps.AutoPRsPerDay, PerWeek: caps.AutoPRsPerWeek, MaxOpen: caps.MaxOpenAutoPRs,
				UsedToday: counts.today, UsedThisWeek: counts.week, OpenNow: counts.open,
			},
			RepoHasAutoPR: repoHoldsAutonomy(all, c.Repo) ||
				(previewRepos[strings.ToLower(c.Repo)] && !mergedOn(all, c.Repo)),
			CLASigned:     cfg.CLASigned(c.Repo),
		}
		d := autogate.Decide(in)
		if !isSeedRepo(prof, c.Repo) {
			d.Allowed = false
			d.Blockers = append(d.Blockers, notSeedBlocker)
		}
		if !matched {
			// Say it once. With no domain, autogate's grant and profile gates
			// both report on a domain with no name, which explains nothing.
			kept := []string{"matches no domain in the profile"}
			for _, b := range d.Blockers {
				if !strings.Contains(b, `domain ""`) && !strings.Contains(b, `for ""`) {
					kept = append(kept, b)
				}
			}
			d.Allowed, d.Blockers = false, kept
		}

		switch {
		case d.Allowed:
			fmt.Printf("APPROVE  %s  (%s)\n", c.Slug(), domain.ID)
			if !execute {
				// A preview has to make the same later decisions the real run
				// would, so it takes the slot and the repository on a copy.
				approved++
				counts.today++
				counts.week++
				counts.open++
				previewRepos[strings.ToLower(c.Repo)] = true
				continue
			}
			if err := autogate.Approve(c, d, now); err != nil {
				fmt.Fprintf(os.Stderr, "  %v\n", err)
				problems++
				continue
			}
			if _, err := st.Save(c); err != nil {
				fmt.Fprintf(os.Stderr, "  save: %v\n", err)
				problems++
				continue
			}
			_ = log.Record("auto_approve", c.Slug(), fmt.Sprintf("autogate, domain %s: %s", domain.ID, d.Why()))
			approved++
			// The next decision in this run must see the slot this one took.
			counts.today++
			counts.week++
			counts.open++
			all, _ = st.All()

		case onlyTemporary(d.Blockers):
			fmt.Printf("HOLD     %s  (%s)\n", c.Slug(), d.Why())
			held++

		default:
			fmt.Printf("REJECT   %s  (%s)\n", c.Slug(), d.Why())
			if !execute {
				rejected++
				continue
			}
			reason := "autonomous gate refused: " + d.Why()
			c.RejectReason = reason
			if err := model.Transition(c, model.StatusRejected, reason); err != nil {
				fmt.Fprintf(os.Stderr, "  %v\n", err)
				problems++
				continue
			}
			if _, err := st.Save(c); err != nil {
				fmt.Fprintf(os.Stderr, "  save: %v\n", err)
				problems++
				continue
			}
			_ = log.Record("auto_reject", c.Slug(), d.Why())
			rejected++
		}
	}

	mode := "dry run, nothing written; --execute to act"
	if execute {
		mode = "written"
	}
	fmt.Printf("\n%d proposal(s): %d approved, %d rejected, %d held (%s)\n",
		len(queue), approved, rejected, held, mode)
	if problems > 0 {
		return 1
	}
	return 0
}

// standingGrant is the operator's standing authorisation in the shape
// autogate checks. The expiry is a day ahead and re-derived every run, so the
// grant lasts exactly as long as the policy says "standing" -- switching it
// off stops the next run, not one ninety days later.
func standingGrant(domain string, since time.Time, trial bool, now time.Time) autogate.Grant {
	trialStart := since
	if !trial {
		// Skipping the silent period is the operator's explicit choice, so the
		// trial must be over whatever the clocks say. Measured from now, not
		// from since: since is a calendar date read as UTC midnight, which
		// for an operator east of Greenwich is still in the future on the
		// evening they set it -- and that put a "no trial" grant in its trial.
		trialStart = now.Add(-(autogate.TrialDays + 1) * 24 * time.Hour)
	}
	return autogate.Grant{
		Domain: domain, Level: profile.AutonomyOpenPRs,
		GrantedAt: since, ExpiresAt: now.Add(24 * time.Hour),
		Countersigned: true, CountersignedAt: since,
		TrialStartedAt: trialStart,
	}
}

// liveAuto is every status in which an autonomous approval still occupies a
// slot: approved but not yet built, being built, or a pull request that has
// not been decided.
var liveAuto = map[model.Status]bool{
	model.StatusAutoApproved: true, model.StatusImplementing: true, model.StatusImplemented: true,
	model.StatusPushed: true, model.StatusPROpen: true, model.StatusChangesRequested: true,
	model.StatusUpdating: true, model.StatusStale: true,
}

type autoCounts struct{ today, week, open int }

// countAuto counts autonomous approvals, from history rather than current
// status: one approved this morning and already merged still spent today's
// budget.
func countAuto(all []*model.Candidate, now time.Time) autoCounts {
	var n autoCounts
	day := now.UTC().Format("2006-01-02")
	for _, c := range all {
		at, ok := autoApprovedAt(c)
		if !ok {
			continue
		}
		if at.UTC().Format("2006-01-02") == day {
			n.today++
		}
		if now.Sub(at) < 7*24*time.Hour {
			n.week++
		}
		if liveAuto[c.Status] {
			n.open++
		}
	}
	return n
}

// repoHoldsAutonomy is the ramp: until something of ours has merged on a
// repository, it gets one autonomous pull request at a time, and one that was
// closed unmerged stops autonomous work there altogether. Maintainers judge an
// unknown contributor by the first pull request, and a second unattended one
// before they have answered the first is how a contributor becomes noise.
// After a merge, the ordinary per-repository cap applies.
func repoHoldsAutonomy(all []*model.Candidate, repo string) bool {
	merged, holding := false, false
	for _, c := range all {
		if !strings.EqualFold(c.Repo, repo) {
			continue
		}
		if c.Status == model.StatusMerged {
			merged = true
		}
		if _, auto := autoApprovedAt(c); auto && (liveAuto[c.Status] || c.Status == model.StatusClosed) {
			holding = true
		}
	}
	return holding && !merged
}

// onlyTemporary reports whether every refusal will clear by itself. Matched
// on the wording autogate uses for exactly these gates, which its own tests
// pin.
func onlyTemporary(blockers []string) bool {
	if len(blockers) == 0 {
		return false
	}
	for _, b := range blockers {
		// The silent trial is the clearest case: its whole point is that the
		// answer is "not yet", and rejecting on it would throw away every
		// proposal the trial exists to observe.
		if !strings.HasPrefix(b, "autonomous cap reached") &&
			b != "this repository already has an autonomous PR" &&
			!strings.HasPrefix(b, "HALT is set") &&
			!strings.HasPrefix(b, "silent trial") {
			return false
		}
	}
	return true
}

// domainRecord is the merge record autogate's probation reads, for the
// domain each decided pull request's repository belongs to.
func domainRecord(all []*model.Candidate, prof *profile.Profile, domain string) autogate.DomainRecord {
	var r autogate.DomainRecord
	for _, c := range all {
		if c.Status != model.StatusMerged && c.Status != model.StatusClosed {
			continue
		}
		lang, topics := "", []string(nil)
		if c.Facts != nil {
			lang, topics = c.Facts.PrimaryLanguage, c.Facts.Topics
		}
		if d, ok := prof.ForRepo(c.Repo, lang, topics); !ok || d.ID != domain {
			continue
		}
		if c.Status == model.StatusMerged {
			r.Merged++
		} else {
			r.Closed++
		}
	}
	return r
}

// notSeedBlocker refuses autonomy on any repository the operator did not name.
//
// The profile's seed repositories are the operator's review of where the
// pipeline may work. Discovery sweeps only those today; open search is
// configured but not built. internal/sources describes a quarantine for
// repositories it would find -- the first unattended pull request into a
// project nobody looked at is the one that should not happen -- but nothing
// records when such a repository was first seen, so the quarantine cannot be
// measured. Until it can, a repository that is not a seed gets no autonomous
// pull request at all, rather than one on its first day.
const notSeedBlocker = "not a seed repository in the profile; autonomy needs the operator to have named it"

func isSeedRepo(prof *profile.Profile, repo string) bool {
	for _, rd := range prof.SeedRepos() {
		if strings.EqualFold(rd.Repo, repo) {
			return true
		}
	}
	return false
}

// mergedOn reports whether anything of ours has merged on repo, which ends
// the one-at-a-time ramp there.
func mergedOn(all []*model.Candidate, repo string) bool {
	for _, c := range all {
		if strings.EqualFold(c.Repo, repo) && c.Status == model.StatusMerged {
			return true
		}
	}
	return false
}

func autoApprovedAt(c *model.Candidate) (time.Time, bool) {
	for i := len(c.History) - 1; i >= 0; i-- {
		if c.History[i].To == string(model.StatusAutoApproved) {
			t, err := time.Parse(time.RFC3339, c.History[i].At)
			return t, err == nil
		}
	}
	return time.Time{}, false
}

func proposedAt(c *model.Candidate) string {
	for i := len(c.History) - 1; i >= 0; i-- {
		if c.History[i].To == string(model.StatusProposed) {
			return c.History[i].At
		}
	}
	return ""
}
