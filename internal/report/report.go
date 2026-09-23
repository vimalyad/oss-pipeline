// Package report renders the single status page, regenerated on every watch
// cycle.
//
// The pipeline already writes proposal reports and a ledger, and neither
// answers the question actually being asked day to day: what is happening with
// my open pull requests, and is anything waiting on me? Logs do not answer it
// either -- they are append-only, and the present has to be reconstructed from
// them.
//
// Rendering is separated from gathering so the layout can be tested without a
// network, and so a repository that fails to read degrades to one line instead
// of taking the page down.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vimalyad/osspipeline/internal/ledger"

	"github.com/vimalyad/osspipeline/internal/text"
)

// PRView is one open pull request as the page needs it.
type PRView struct {
	Repo      string
	Number    int
	Title     string
	URL       string
	State     string
	Merged    bool
	Review    string
	Mergeable string

	CheckCounts map[string]int
	Failing     []string

	LastCommentBy string
	LastCommentAt string

	PendingReplies int
	DraftedReplies int

	// Err is set when this pull request could not be read. One unreadable
	// repository must cost one line, not the whole page.
	Err string
}

// Item is something waiting on the user, or something already queued.
type Item struct {
	Text string
	// Cmd is the command that resolves it, when one exists. A blocker has
	// none: it needs a human action outside the pipeline.
	Cmd string
}

// Input is everything the page renders from.
type Input struct {
	Generated time.Time
	// Login is the pipeline's own GitHub account, used to tell the user's own
	// comments from a maintainer's.
	Login    string
	Open     []PRView
	NeedsYou []Item
	Queued   []Item
	Ledger   ledger.Ledger
}

// Render produces the page.
func Render(in Input) string {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }

	p("# Pipeline status")
	p("")
	p("_generated %s UTC_", in.Generated.UTC().Format("2006-01-02 15:04"))
	p("")

	// Attention first, because it is the reason the page is opened.
	if att := attention(in); len(att) > 0 {
		p("## Needs attention")
		p("")
		for _, a := range att {
			p("- %s", a)
		}
		p("")
	} else {
		p("_Nothing needs your attention._")
		p("")
	}

	p("## Open pull requests")
	p("")
	if len(in.Open) == 0 {
		p("_none open_")
		p("")
	}
	for _, pr := range in.Open {
		if pr.Err != "" {
			p("- **%s#%d** — could not read: %s", pr.Repo, pr.Number, text.Clip(pr.Err, 80))
			p("")
			continue
		}
		p("### [%s#%d](%s)", pr.Repo, pr.Number, pr.URL)
		p("_%s_", pr.Title)
		p("")
		merged := ""
		if pr.Merged {
			merged = " · **MERGED**"
		}
		p("- state **%s**%s · review: **%s** · mergeable: %s",
			pr.State, merged, orElse(pr.Review, "no review yet"), orElse(pr.Mergeable, "unknown"))
		p("- CI: %s", checkSummary(pr.CheckCounts))
		if len(pr.Failing) > 0 {
			p("- **FAILING:** %s", strings.Join(first(pr.Failing, 6), ", "))
		}
		if pr.LastCommentBy != "" {
			p("- last comment: **%s** at %s", pr.LastCommentBy, pr.LastCommentAt)
		}
		if pr.PendingReplies > 0 {
			p("- **%d item(s) awaiting your reply** (%d drafted) — `pipeline replies`",
				pr.PendingReplies, pr.DraftedReplies)
		}
		p("")
	}

	// Two different things, kept apart on purpose. A section headed "needs
	// you" that also lists machine-queued work teaches the reader to skip the
	// section, which is exactly when a real request gets missed.
	if len(in.NeedsYou) > 0 {
		p("## Needs you")
		p("")
		for _, it := range in.NeedsYou {
			p("- %s", it.Text)
			if it.Cmd != "" {
				p("  `%s`", it.Cmd)
			}
		}
		p("")
	}
	if len(in.Queued) > 0 {
		p("## Queued — no action needed")
		p("")
		for _, it := range in.Queued {
			p("- %s", it.Text)
			if it.Cmd != "" {
				p("  %s", it.Cmd)
			}
		}
		p("")
	}

	p("## Totals")
	p("")
	c := in.Ledger.Counts
	p("- merged **%d** · closed %d · open %d · tracked %d", c.Merged, c.Closed, c.Open, c.Tracked)
	p("- merge rate %.0f%%", in.Ledger.MergeRate*100)
	p("")
	if len(in.Ledger.Inconsistent) > 0 {
		p("- %d record(s) disagree with their own history — see the ledger",
			len(in.Ledger.Inconsistent))
		p("")
	}
	return b.String()
}

// attention is the short list at the top: only things a person has to act on.
func attention(in Input) []string {
	var out []string
	for _, pr := range in.Open {
		if pr.Err != "" {
			continue
		}
		if len(pr.Failing) > 0 {
			out = append(out, fmt.Sprintf("CI failing on %s#%d: %s", pr.Repo, pr.Number, pr.Failing[0]))
		}
		// Our own comment is not news. Reporting it trains the reader to
		// ignore the line that matters.
		if pr.LastCommentBy != "" && !strings.EqualFold(pr.LastCommentBy, in.Login) {
			out = append(out, fmt.Sprintf("%s commented on %s#%d", pr.LastCommentBy, pr.Repo, pr.Number))
		}
		if pr.PendingReplies > 0 {
			out = append(out, fmt.Sprintf("%d reply(ies) queued on %s#%d",
				pr.PendingReplies, pr.Repo, pr.Number))
		}
	}
	return out
}

func checkSummary(counts map[string]int) string {
	if len(counts) == 0 {
		return "no checks"
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	return strings.Join(parts, ", ")
}

func first(ss []string, n int) []string {
	if len(ss) > n {
		return ss[:n]
	}
	return ss
}

func orElse(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
