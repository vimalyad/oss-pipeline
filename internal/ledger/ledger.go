// Package ledger is the contribution record, and the control signal for
// widening the watchlist: breadth is earned by merged work, not granted by
// time passing.
//
// It is also the number a human reads to decide whether any of this is
// working, which makes an undercount worse than a wrong one. A PR whose
// terminal status was overwritten by hand vanished from both the merge rate
// and the probation count, so Compute reports disagreements between a
// candidate's status and its own history rather than quietly believing the
// status field.
package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vimalyad/osspipeline/internal/model"
)

// Entry is one merged contribution, in the order a reader wants them.
type Entry struct {
	Repo     string `json:"repo"`
	Issue    int    `json:"issue"`
	PR       string `json:"pr"`
	Title    string `json:"title"`
	Stars    int    `json:"stars"`
	Language string `json:"language"`
	Credits  string `json:"credits"`
	MergedAt string `json:"merged_at,omitempty"`
}

type Counts struct {
	Merged  int `json:"merged"`
	Closed  int `json:"closed"`
	Open    int `json:"open"`
	Tracked int `json:"tracked"`
}

type Ledger struct {
	Generated     string  `json:"generated"`
	Merged        []Entry `json:"merged"`
	Counts        Counts  `json:"counts"`
	MergeRate     float64 `json:"merge_rate"`
	Tier2Unlocked bool    `json:"tier2_unlocked"`
	Tier2Needs    string  `json:"tier2_needs"`
	// Inconsistent names candidates whose status disagrees with the last
	// entry in their own history. Every one is a decided PR that is either
	// missing from these counts or counted as something it is not, so it is
	// surfaced rather than silently resolved either way.
	Inconsistent []string `json:"inconsistent,omitempty"`
}

// Unlock is the tier-2 threshold, read from policy.
type Unlock struct {
	MinPRs    int
	MergeRate float64
}

// Compute derives the ledger from every candidate on disk.
func Compute(cands []*model.Candidate, u Unlock, now time.Time) Ledger {
	open := map[model.Status]bool{}
	for _, s := range model.OpenStatuses {
		open[s] = true
	}

	l := Ledger{
		Generated: now.UTC().Format(time.RFC3339),
		Counts:    Counts{Tracked: len(cands)},
	}
	for _, c := range cands {
		if bad := disagreement(c); bad != "" {
			l.Inconsistent = append(l.Inconsistent, bad)
		}
		switch {
		case c.Status == model.StatusMerged:
			l.Counts.Merged++
			l.Merged = append(l.Merged, entryOf(c))
		case c.Status == model.StatusClosed:
			l.Counts.Closed++
		case open[c.Status]:
			l.Counts.Open++
		}
	}
	sort.Slice(l.Merged, func(i, j int) bool {
		if l.Merged[i].MergedAt != l.Merged[j].MergedAt {
			return l.Merged[i].MergedAt > l.Merged[j].MergedAt
		}
		return l.Merged[i].Repo < l.Merged[j].Repo
	})
	sort.Strings(l.Inconsistent)

	decided := l.Counts.Merged + l.Counts.Closed
	if decided > 0 {
		l.MergeRate = round3(float64(l.Counts.Merged) / float64(decided))
	}
	l.Tier2Unlocked = decided >= u.MinPRs && l.MergeRate >= u.MergeRate
	l.Tier2Needs = fmt.Sprintf("%d decided PRs at >=%.0f%%", u.MinPRs, u.MergeRate*100)
	return l
}

// disagreement reports a candidate whose status contradicts its own history.
//
// The history is the audit trail and the status is a cached conclusion, so
// when they differ the status is the one that was written without going
// through the state machine. Naming it is the whole job here: choosing a side
// automatically would hide exactly the edit worth seeing.
func disagreement(c *model.Candidate) string {
	if len(c.History) == 0 {
		return ""
	}
	last := model.Status(c.History[len(c.History)-1].To)
	if last == c.Status || !last.Valid() {
		return ""
	}
	return fmt.Sprintf("%s: status %q but history ends at %q", c.Slug(), c.Status, last)
}

func entryOf(c *model.Candidate) Entry {
	e := Entry{Repo: c.Repo, Issue: c.Issue, PR: c.PRURL, Title: c.Title, Credits: c.Credits}
	if c.Facts != nil {
		e.Stars, e.Language = c.Facts.Stars, c.Facts.PrimaryLanguage
	}
	for i := len(c.History) - 1; i >= 0; i-- {
		if model.Status(c.History[i].To) == model.StatusMerged {
			e.MergedAt = c.History[i].At
			break
		}
	}
	return e
}

func round3(f float64) float64 {
	return float64(int(f*1000+0.5)) / 1000
}

// Markdown renders the ledger for the published report.
func (l Ledger) Markdown() string {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }

	p("# Contribution ledger")
	p("")
	p("_%s_", l.Generated)
	p("")
	p("**%d merged** · %d closed · %d open · %d tracked",
		l.Counts.Merged, l.Counts.Closed, l.Counts.Open, l.Counts.Tracked)
	p("")
	p("Merge rate **%.0f%%**. Tier 2 %s (needs %s).",
		l.MergeRate*100, unlocked(l.Tier2Unlocked), l.Tier2Needs)
	p("")
	if len(l.Merged) == 0 {
		p("_No merged PRs yet._")
		p("")
	} else {
		p("## Merged")
		p("")
		for _, m := range l.Merged {
			credit := ""
			if m.Credits != "" {
				credit = " (with @" + m.Credits + ")"
			}
			p("- [%s#%d](%s) — %s · %s★ %s%s",
				m.Repo, m.Issue, m.PR, m.Title, commas(m.Stars), m.Language, credit)
		}
		p("")
		p("### Resume form")
		p("")
		p("```")
		for _, m := range l.Merged {
			p("%s (%s★) — %s — %s", m.Repo, commas(m.Stars), m.Title, m.PR)
		}
		p("```")
		p("")
	}
	if len(l.Inconsistent) > 0 {
		p("## Needs a look")
		p("")
		p("These records disagree with their own history, which means a decided")
		p("PR is missing from the counts above or counted as something it is not:")
		p("")
		for _, s := range l.Inconsistent {
			p("- %s", s)
		}
		p("")
	}
	return b.String()
}

func unlocked(b bool) string {
	if b {
		return "UNLOCKED"
	}
	return "locked"
}

// commas formats an integer with thousands separators, matching what the
// previous version produced so the published report does not churn.
func commas(n int) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// Write persists both forms: JSON for the pipeline, Markdown for a human.
func Write(root string, l Ledger) (string, error) {
	data := filepath.Join(root, "state", "ledger.json")
	if err := os.MkdirAll(filepath.Dir(data), 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(data, append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	out := filepath.Join(root, "reports", "ledger.md")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(out, []byte(l.Markdown()), 0o644); err != nil {
		return "", err
	}
	return out, nil
}
