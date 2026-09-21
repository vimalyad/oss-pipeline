package report

import (
	"strings"
	"testing"
	"time"

	"github.com/vimalyad/osspipeline/internal/ledger"
)

func base() Input {
	return Input{
		Generated: time.Date(2026, 9, 21, 14, 30, 0, 0, time.UTC),
		Login:     "vimalyad",
		Ledger: ledger.Ledger{
			Counts: ledger.Counts{Merged: 1, Closed: 1, Open: 0, Tracked: 251}, MergeRate: 0.5,
		},
	}
}

func TestQuietPageSaysSoPlainly(t *testing.T) {
	out := Render(base())
	for _, want := range []string{"Nothing needs your attention", "_none open_", "merge rate 50%"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Needs attention") {
		t.Error("a quiet page still printed an attention heading")
	}
}

// TestOwnCommentsAreNotNews: reporting the user's own comment back to them
// trains the reader to skim the section, which is exactly when a maintainer's
// question gets missed.
func TestOwnCommentsAreNotNews(t *testing.T) {
	in := base()
	in.Open = []PRView{{
		Repo: "kornia/kornia", Number: 4455, URL: "u", Title: "t", State: "OPEN",
		LastCommentBy: "vimalyad", LastCommentAt: "2026-09-20 10:00",
	}}
	if got := Render(in); strings.Contains(got, "Needs attention") {
		t.Errorf("our own comment raised attention:\n%s", got)
	}

	in.Open[0].LastCommentBy = "ducha-aiki"
	got := Render(in)
	if !strings.Contains(got, "ducha-aiki commented on kornia/kornia#4455") {
		t.Errorf("a maintainer's comment did not raise attention:\n%s", got)
	}
}

// TestNeedsYouAndQueuedStayApart is the distinction the page exists to keep. A
// section headed "needs you" that also lists machine-queued work teaches the
// reader to skip it.
func TestNeedsYouAndQueuedStayApart(t *testing.T) {
	in := base()
	in.NeedsYou = []Item{{Text: "**a/b#1** approve or reject", Cmd: "pipeline approve a__b__1"}}
	in.Queued = []Item{{Text: "**c/d#2** a title", Cmd: "will run on the next cycle"}}

	out := Render(in)
	needs := strings.Index(out, "## Needs you")
	queued := strings.Index(out, "## Queued")
	if needs < 0 || queued < 0 {
		t.Fatalf("sections missing:\n%s", out)
	}
	if needs > queued {
		t.Error("queued work is printed above the work that needs a person")
	}
	section := out[needs:queued]
	if strings.Contains(section, "c/d#2") {
		t.Error("queued work leaked into the Needs you section")
	}
}

// TestAnUnreadablePRCostsOneLine: one repository failing must not take the
// page down, because the page is how the user finds out anything at all.
func TestAnUnreadablePRCostsOneLine(t *testing.T) {
	in := base()
	in.Open = []PRView{
		{Repo: "a/b", Number: 1, Err: "graphql: rate limited"},
		{Repo: "kornia/kornia", Number: 4455, URL: "u", Title: "t", State: "OPEN",
			Failing: []string{"tests (3.11)"}},
	}
	out := Render(in)
	if !strings.Contains(out, "a/b#1** — could not read: graphql: rate limited") {
		t.Errorf("the failure was not reported:\n%s", out)
	}
	if !strings.Contains(out, "kornia/kornia#4455") {
		t.Error("a later PR was lost because an earlier one failed")
	}
	if !strings.Contains(out, "CI failing on kornia/kornia#4455: tests (3.11)") {
		t.Error("the readable PR's failure did not reach the attention list")
	}
	// An unreadable PR is not a failing one; claiming CI is red would be a
	// statement we cannot support.
	if strings.Contains(out, "CI failing on a/b#1") {
		t.Error("an unreadable PR was reported as failing CI")
	}
}

func TestCheckSummaryIsDeterministic(t *testing.T) {
	counts := map[string]int{"success": 38, "failure": 2, "pending": 1}
	first := checkSummary(counts)
	for i := 0; i < 50; i++ {
		if got := checkSummary(counts); got != first {
			t.Fatalf("varied: %q vs %q", got, first)
		}
	}
	if first != "2 failure, 1 pending, 38 success" {
		t.Errorf("= %q", first)
	}
	if got := checkSummary(nil); got != "no checks" {
		t.Errorf("empty = %q", got)
	}
}

func TestFailingListIsCapped(t *testing.T) {
	in := base()
	var many []string
	for i := 0; i < 20; i++ {
		many = append(many, "job")
	}
	in.Open = []PRView{{Repo: "a/b", Number: 1, URL: "u", State: "OPEN", Failing: many}}
	out := Render(in)
	line := lineWith(out, "**FAILING:**")
	if strings.Count(line, "job") != 6 {
		t.Errorf("printed %d of 20 failing checks: %q", strings.Count(line, "job"), line)
	}
}

func TestLedgerDisagreementsSurfaceOnThePage(t *testing.T) {
	in := base()
	in.Ledger.Inconsistent = []string{"cli__cli__14386: status \"rejected\" but history ends at \"closed\""}
	if !strings.Contains(Render(in), "disagree with their own history") {
		t.Error("a ledger inconsistency is invisible on the status page")
	}
}

func TestPendingRepliesAppearWithTheirDraftCount(t *testing.T) {
	in := base()
	in.Open = []PRView{{Repo: "a/b", Number: 1, URL: "u", State: "OPEN",
		PendingReplies: 3, DraftedReplies: 1}}
	out := Render(in)
	if !strings.Contains(out, "3 item(s) awaiting your reply** (1 drafted)") {
		t.Errorf("reply counts missing:\n%s", out)
	}
	if !strings.Contains(out, "3 reply(ies) queued on a/b#1") {
		t.Error("queued replies did not reach the attention list")
	}
}

func lineWith(s, needle string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return ""
}
