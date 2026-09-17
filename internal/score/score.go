// Package score decides whether a candidate is worth proposing.
//
// It is a gate, not a number. Every bar must be cleared, and every failure is
// recorded with the reason, because the report has to justify a rejection to
// the person reading it on a phone.
//
// Three outcomes, deliberately distinct:
//
//	fails     -- reject. Something about this issue makes a PR unwelcome.
//	penalties -- accept, but rank below cleaner candidates.
//	blockers  -- accept, but a human must do one thing first (sign a CLA).
package score

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vimalyad/osspipeline/internal/model"
	"github.com/vimalyad/osspipeline/internal/policy"
)

var docsOnly = regexp.MustCompile(`(?i)\b(typo|spelling|grammar|broken link|dead link|readme|changelog)\b`)

var docsLabels = map[string]bool{
	"documentation": true, "docs": true, "typo": true,
	"good first issue: docs": true,
}

// Result is everything the scorer concluded, kept separate from the candidate
// so the same inputs can be re-scored without mutating stored state.
type Result struct {
	OK        bool
	Fails     []string
	Penalties []string
	Blockers  []string
}

// Deps are the few things the scorer cannot work out for itself. Declared as
// an interface at the point of use rather than in a shared package: the
// scorer needs exactly this much of the world and no more.
type Deps struct {
	// MissingToolchain reports the binary a language needs but this machine
	// lacks, or "". Replaced by container recipe resolution later; kept for
	// now so Go and Python produce identical verdicts during the port.
	MissingToolchain func(language string) string
	// LoadFacts fetches cached repo facts when a candidate has none embedded.
	// Declared here rather than imported so the scorer depends on the one
	// method it needs, not on a store.
	LoadFacts func(repo string) *model.RepoFacts
	Now       func() time.Time
}

// MaintainerAccepted reports whether someone with triage rights accepted the
// issue.
//
// Label-based, because outside contributors cannot apply labels: a
// `good first issue` label is a maintainer action by construction. A
// maintainer comment stating an approach also counts. An explicit untriaged
// label vetoes both -- `triage/pending` means precisely that nobody decided.
func MaintainerAccepted(c *model.Candidate, p *policy.Policy) (bool, string) {
	have := lowerSet(c.Labels)
	if hit := firstMatch(have, p.UntriagedLabels); hit != "" {
		return false, fmt.Sprintf("carries untriaged label %s", pyRepr(hit))
	}
	if hit := firstMatch(have, p.AcceptanceLabels); hit != "" {
		return true, fmt.Sprintf("maintainer applied %s", pyRepr(hit))
	}
	if c.Brief != nil && c.Brief.MaintainerDesiredApproach != "" {
		return true, fmt.Sprintf("maintainer stated an approach (%s)",
			c.Brief.ApproachAuthorAssociation)
	}
	return false, "no triage-gated label and no maintainer statement"
}

// Score applies every bar. The candidate is not mutated.
func Score(c *model.Candidate, cfg *policy.Config, otherLogins []string, d Deps) Result {
	if d.Now == nil {
		d.Now = time.Now
	}
	p := &cfg.Policy
	var r Result

	// --- contest ---------------------------------------------------------
	switch {
	case c.Contest == "":
		r.Fails = append(r.Fails, "not classified (contest.py did not run)")
	case !c.Contest.Targetable():
		r.Fails = append(r.Fails,
			fmt.Sprintf("contest class is %s (targetable: no_pr, stale_pr)", c.Contest))
	}

	// --- cross-account ---------------------------------------------------
	// Two PRs on one issue from two accounts sharing a display name reads as
	// sockpuppeting, however innocent the intent.
	if cfg.TouchedByOtherAccounts[c.Repo] {
		r.Fails = append(r.Fails, fmt.Sprintf(
			"%s already touched by another of your accounts (%s) -- linkable, see plan section 7",
			c.Repo, strings.Join(otherLogins, ", ")))
	}
	if cfg.Excluded(c.Repo) {
		r.Fails = append(r.Fails, fmt.Sprintf("%s is manually excluded", c.Repo))
	}

	// --- repo facts ------------------------------------------------------
	f := c.Facts
	if f == nil && d.LoadFacts != nil {
		f = d.LoadFacts(c.Repo)
	}
	if f == nil {
		r.Fails = append(r.Fails, "no repo facts (repofacts did not run)")
	} else {
		if f.BansAIPRs {
			r.Fails = append(r.Fails, fmt.Sprintf("repo bans AI-assisted PRs: %s",
				pyRepr(truncate(f.AIPolicyQuote, 120))))
		}
		// Soft, not fatal: CONTRIBUTING.md is only a proxy for "accepts
		// outside contributions", and merged_first_time_pr_90d measures that
		// directly. A repo that demonstrably merges newcomer PRs without one
		// is fine -- it just ranks below a repo that documents its process.
		if !f.HasContributing {
			r.Penalties = append(r.Penalties, "no CONTRIBUTING.md (ranked lower)")
		}
		if p.Scoring.RequireTests && !f.HasTests {
			r.Fails = append(r.Fails, "no test suite (CI cannot act as a correctness oracle)")
		}
		if !f.MergedFirstTimePR90d {
			r.Fails = append(r.Fails, fmt.Sprintf(
				"no PR from an outside contributor merged in %dd -- unreceptive",
				p.Scoring.FirstTimeWindowDays))
		}
		if d.MissingToolchain != nil {
			if missing := d.MissingToolchain(f.PrimaryLanguage); missing != "" {
				r.Blockers = append(r.Blockers, fmt.Sprintf(
					"%s toolchain missing -- install `%s` (e.g. brew install %s) "+
						"before this can be built or tested",
					f.PrimaryLanguage, missing, missing))
			}
		}
		if f.RequiresCLA && !cfg.CLASigned(c.Repo) {
			r.Blockers = append(r.Blockers, fmt.Sprintf(
				"CLA required for %s -- sign once, then `pipeline cla-signed %s`",
				c.Owner(), c.Repo))
		}

		// --- project eligibility rules -----------------------------------
		// cli/cli accepts outside PRs only for `help wanted` issues, and
		// closed one of ours for exactly this.
		have := lowerSet(c.Labels)
		for _, req := range f.RequiredIssueLabels {
			if !have[strings.ToLower(req)] {
				r.Fails = append(r.Fails, fmt.Sprintf(
					"%s accepts outside PRs only for issues labelled %s; this issue has %s",
					c.Repo, pyRepr(req), pyList(sortedKeys(have))))
				break
			}
		}
		for _, ban := range f.ForbiddenIssueLabels {
			if have[strings.ToLower(ban)] {
				r.Fails = append(r.Fails, fmt.Sprintf(
					"%s does not accept PRs for issues labelled %s", c.Repo, pyRepr(ban)))
				break
			}
		}
	}

	// --- thread ----------------------------------------------------------
	b := c.Brief
	if b == nil {
		r.Fails = append(r.Fails, "no brief (harvest/brief did not run)")
	} else {
		if accepted, why := MaintainerAccepted(c, p); p.Scoring.RequireMaintainerAcceptance && !accepted {
			r.Fails = append(r.Fails, fmt.Sprintf("no maintainer acceptance (%s)", why))
		}
		if p.Scoring.RequireApproachOrCriteria &&
			b.MaintainerDesiredApproach == "" && len(b.AcceptanceCriteria) == 0 {
			r.Fails = append(r.Fails,
				"no stated approach AND no acceptance criteria -- a patch here would be guesswork")
		}
		// An unconverged thread is the single biggest cause of a rejected PR:
		// no patch can be right while the design is still being argued.
		if p.Scoring.RequireConvergedThread && len(b.OpenQuestions) > 0 {
			r.Fails = append(r.Fails, fmt.Sprintf(
				"thread has not converged: %d open question(s) -- %s",
				len(b.OpenQuestions), pyRepr(truncate(b.OpenQuestions[0], 90))))
		}
		if b.ClaimedBy != "" {
			recent := true
			if b.ClaimedAt != "" {
				if when, err := parseTime(b.ClaimedAt); err == nil {
					recent = int(d.Now().UTC().Sub(when).Hours()/24) <= p.Staleness.ClaimHonouredDays
				}
			}
			if recent {
				r.Fails = append(r.Fails,
					fmt.Sprintf("claimed by @%s -- respect the claim", b.ClaimedBy))
			}
		}
	}

	// --- age -------------------------------------------------------------
	// A maintainer's stated approach ages with the codebase. Not fatal --
	// plenty of good-first-issues sit for years -- but it ranks lower, and
	// the report says how old it is so the reading can be checked.
	if c.IssueCreatedAt != "" {
		if opened, err := parseTime(c.IssueCreatedAt); err == nil {
			ageDays := int(d.Now().UTC().Sub(opened).Hours() / 24)
			if ageDays > p.Scoring.StaleIssuePenaltyYears*365 {
				r.Penalties = append(r.Penalties, fmt.Sprintf(
					"issue opened %dy ago (%s); the stated approach may predate the current code",
					ageDays/365, opened.Format("2006-01")))
			}
		}
	}

	// --- shape of the work -----------------------------------------------
	if p.Scoring.RejectDocsTypoOnly {
		labels := lowerSet(c.Labels)
		docsLabelled := false
		for l := range labels {
			if docsLabels[l] {
				docsLabelled = true
				break
			}
		}
		if docsOnly.MatchString(c.Title) || (docsLabelled && c.Comments <= 1) {
			r.Fails = append(r.Fails, "looks docs/typo-only -- noise in a mature repo")
		}
	}

	r.OK = len(r.Fails) == 0
	return r
}

func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(strings.Replace(s, "Z", "+00:00", 1))
	for _, layout := range []string{
		"2006-01-02T15:04:05-07:00", "2006-01-02T15:04:05.999999-07:00",
		"2006-01-02T15:04:05", "2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable time %q", s)
}

func lowerSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[strings.ToLower(s)] = true
	}
	return m
}

// firstMatch returns the alphabetically first label present in both sets,
// matching the Python implementation's sorted() so messages are identical.
func firstMatch(have map[string]bool, want []string) string {
	var hits []string
	for _, w := range want {
		if have[strings.ToLower(w)] {
			hits = append(hits, strings.ToLower(w))
		}
	}
	if len(hits) == 0 {
		return ""
	}
	sort.Strings(hits)
	return hits[0]
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// pyList renders a string slice the way Python prints a list, so failure
// messages compare byte-for-byte against the implementation being replaced.
// pyRepr quotes a string the way Python's repr() does: single quotes, unless
// the value contains an apostrophe and no double quote, in which case Python
// switches to double quotes. Reproducing that heuristic is what makes the
// failure messages diff cleanly against the implementation being replaced.
func pyRepr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch r {
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		case '\\':
			b.WriteString(`\\`)
		case rune(quote):
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

func pyList(ss []string) string {
	if len(ss) == 0 {
		return "[]"
	}
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = pyRepr(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}
