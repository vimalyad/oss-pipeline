package ledger

import (
	"strings"
	"testing"
	"time"

	"github.com/vimalyad/osspipeline/internal/model"
)

func cand(repo string, issue int, st model.Status, hist ...model.Status) *model.Candidate {
	c := &model.Candidate{Repo: repo, Issue: issue, Status: st, Title: "t", PRURL: "u"}
	for i, h := range hist {
		c.History = append(c.History, model.HistoryEntry{
			At: time.Date(2026, 9, 10+i, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
			To: string(h),
		})
	}
	return c
}

var unlock = Unlock{MinPRs: 6, MergeRate: 0.5}

func TestCounts(t *testing.T) {
	cands := []*model.Candidate{
		cand("a/a", 1, model.StatusMerged, model.StatusMerged),
		cand("b/b", 2, model.StatusClosed, model.StatusClosed),
		cand("c/c", 3, model.StatusPROpen, model.StatusPROpen),
		cand("d/d", 4, model.StatusRejected, model.StatusRejected),
	}
	l := Compute(cands, unlock, time.Now())
	if l.Counts != (Counts{Merged: 1, Closed: 1, Open: 1, Tracked: 4}) {
		t.Fatalf("counts = %+v", l.Counts)
	}
	if l.MergeRate != 0.5 {
		t.Errorf("merge rate = %v, want 0.5", l.MergeRate)
	}
	if l.Tier2Unlocked {
		t.Error("tier 2 unlocked on 2 decided PRs")
	}
}

// TestADisagreementIsReportedNotResolved is the defect this package was
// rewritten around. A hand edit overwrote a closed PR's terminal status with
// "rejected", so the record said one thing and its history another; the PR
// vanished from the merge rate and from the probation count, and the ledger
// read 1 merged / 0 closed with no sign anything was wrong.
func TestADisagreementIsReportedNotResolved(t *testing.T) {
	c := cand("cli/cli", 14386, model.StatusRejected, model.StatusPROpen, model.StatusClosed)
	l := Compute([]*model.Candidate{c}, unlock, time.Now())

	if len(l.Inconsistent) != 1 {
		t.Fatalf("Inconsistent = %v, want the disagreement named", l.Inconsistent)
	}
	got := l.Inconsistent[0]
	for _, want := range []string{"cli__cli__14386", "rejected", "closed"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q does not mention %q", got, want)
		}
	}
	// Reporting it must not also mean guessing. Believing the history here
	// would hide the edit just as effectively as believing the status.
	if l.Counts.Closed != 0 {
		t.Error("the ledger picked a side instead of reporting the conflict")
	}
	if !strings.Contains(l.Markdown(), "Needs a look") {
		t.Error("the disagreement is not visible in the rendered report")
	}
}

func TestConsistentRecordsAreNotFlagged(t *testing.T) {
	cands := []*model.Candidate{
		cand("a/a", 1, model.StatusMerged, model.StatusPROpen, model.StatusMerged),
		cand("b/b", 2, model.StatusRejected, model.StatusDiscovered, model.StatusRejected),
		// No history at all is a legitimate shape for a freshly discovered
		// candidate and must not be reported as a conflict.
		{Repo: "c/c", Issue: 3, Status: model.StatusDiscovered},
	}
	if l := Compute(cands, unlock, time.Now()); len(l.Inconsistent) != 0 {
		t.Fatalf("Inconsistent = %v", l.Inconsistent)
	}
}

func TestTier2NeedsBothThresholds(t *testing.T) {
	tests := []struct {
		name           string
		merged, closed int
		want           bool
	}{
		{"not enough decided", 3, 0, false},
		{"enough decided, rate too low", 2, 4, false},
		{"exactly at both thresholds", 3, 3, true},
		{"clear of both", 6, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cands []*model.Candidate
			for i := 0; i < tt.merged; i++ {
				cands = append(cands, cand("m/m", i, model.StatusMerged, model.StatusMerged))
			}
			for i := 0; i < tt.closed; i++ {
				cands = append(cands, cand("c/c", i, model.StatusClosed, model.StatusClosed))
			}
			if got := Compute(cands, unlock, time.Now()).Tier2Unlocked; got != tt.want {
				t.Errorf("unlocked = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEmptyLedgerDoesNotDivideByZero(t *testing.T) {
	l := Compute(nil, unlock, time.Now())
	if l.MergeRate != 0 || l.Tier2Unlocked {
		t.Fatalf("%+v", l)
	}
	if !strings.Contains(l.Markdown(), "No merged PRs yet") {
		t.Error("empty ledger renders badly")
	}
}

func TestMergedAreNewestFirst(t *testing.T) {
	old := cand("a/a", 1, model.StatusMerged, model.StatusMerged)
	recent := cand("b/b", 2, model.StatusMerged, model.StatusPROpen, model.StatusMerged)
	l := Compute([]*model.Candidate{old, recent}, unlock, time.Now())
	if len(l.Merged) != 2 || l.Merged[0].Repo != "b/b" {
		t.Fatalf("order = %+v", l.Merged)
	}
	if l.Merged[0].MergedAt == "" {
		t.Error("merged entries should carry the date from their history")
	}
}

func TestCommas(t *testing.T) {
	for _, tt := range []struct {
		in   int
		want string
	}{{0, "0"}, {999, "999"}, {1000, "1,000"}, {11346, "11,346"}, {1234567, "1,234,567"}} {
		if got := commas(tt.in); got != tt.want {
			t.Errorf("commas(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
