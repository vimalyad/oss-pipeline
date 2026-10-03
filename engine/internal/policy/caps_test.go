package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/vimalyad/oss-pipeline/engine/internal/model"
)

func at(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05-07:00") }

func cand(repo string, issue int, status model.Status, opened ...time.Time) *model.Candidate {
	c := &model.Candidate{Repo: repo, Issue: issue, Status: status}
	for _, o := range opened {
		c.History = append(c.History, model.HistoryEntry{
			At: at(o), From: string(model.StatusPushed), To: string(model.StatusPROpen),
		})
	}
	return c
}

func joined(reasons []string) string { return strings.Join(reasons, " | ") }

var testCaps = Caps{PRsPerDay: 2, MaxOpenPRs: 5, MaxOpenPerRepo: 1, OrgCooldownDays: 3}

func TestCapsClearWhenNothingIsOpen(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	target := cand("helm/helm", 13284, model.StatusApproved)
	if r := testCaps.Check([]*model.Candidate{target}, target, now); len(r) > 0 {
		t.Fatalf("expected no reasons, got %s", joined(r))
	}
}

func TestCapsCountEveryOpenStatus(t *testing.T) {
	// Not just pr_open. A stale or changes-requested pull request is still
	// open on GitHub and still visible to the maintainers who have to triage
	// it, so it has to occupy a slot.
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	caps := Caps{PRsPerDay: 9, MaxOpenPRs: 5, MaxOpenPerRepo: 9}
	all := []*model.Candidate{
		cand("a/one", 1, model.StatusPushed),
		cand("b/two", 2, model.StatusPROpen),
		cand("c/three", 3, model.StatusChangesRequested),
		cand("d/four", 4, model.StatusUpdating),
		cand("e/five", 5, model.StatusStale),
	}
	target := cand("f/six", 6, model.StatusApproved)
	r := caps.Check(append(all, target), target, now)
	if len(r) != 1 || !strings.Contains(r[0], "5 pull requests already open") {
		t.Fatalf("expected the open cap to bite at 5, got %s", joined(r))
	}
}

func TestCapsOnePerRepo(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	all := []*model.Candidate{cand("helm/helm", 999, model.StatusPROpen)}
	target := cand("helm/helm", 13284, model.StatusApproved)
	r := testCaps.Check(append(all, target), target, now)
	if len(r) != 1 || !strings.Contains(r[0], "helm/helm") {
		t.Fatalf("expected the per-repo cap, got %s", joined(r))
	}
}

func TestCapsDailyBudgetSpentByAMergedPR(t *testing.T) {
	// Opened at 09:00 and merged by lunchtime. The status is terminal, so
	// counting open pull requests would refund the day's budget; counting
	// history does not.
	now := time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)
	morning := now.Add(-9 * time.Hour)
	caps := Caps{PRsPerDay: 2, MaxOpenPRs: 5, MaxOpenPerRepo: 1}
	all := []*model.Candidate{
		cand("a/one", 1, model.StatusMerged, morning),
		cand("b/two", 2, model.StatusClosed, morning),
	}
	target := cand("helm/helm", 13284, model.StatusApproved)
	r := caps.Check(append(all, target), target, now)
	if len(r) != 1 || !strings.Contains(r[0], "2 pull requests opened today") {
		t.Fatalf("expected the daily cap, got %s", joined(r))
	}
}

func TestCapsDailyBudgetResetsAtMidnightUTC(t *testing.T) {
	now := time.Date(2026, 9, 30, 0, 30, 0, 0, time.UTC)
	yesterday := now.Add(-2 * time.Hour)
	caps := Caps{PRsPerDay: 2, MaxOpenPRs: 5, MaxOpenPerRepo: 1}
	all := []*model.Candidate{
		cand("a/one", 1, model.StatusMerged, yesterday),
		cand("b/two", 2, model.StatusMerged, yesterday),
	}
	target := cand("helm/helm", 13284, model.StatusApproved)
	if r := caps.Check(append(all, target), target, now); len(r) > 0 {
		t.Fatalf("yesterday's pull requests should not count, got %s", joined(r))
	}
}

func TestCapsOneCandidateCountsOnceADay(t *testing.T) {
	// A pull request that went stale and came back reaches pr_open twice. It
	// is still one pull request against the day's budget.
	now := time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)
	caps := Caps{PRsPerDay: 2, MaxOpenPRs: 5, MaxOpenPerRepo: 1}
	twice := cand("a/one", 1, model.StatusPROpen, now.Add(-9*time.Hour), now.Add(-2*time.Hour))
	target := cand("helm/helm", 13284, model.StatusApproved)
	if r := caps.Check([]*model.Candidate{twice, target}, target, now); len(r) > 0 {
		t.Fatalf("expected no reasons, got %s", joined(r))
	}
}

func TestCapsOrgCooldown(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	n := 4455
	other := cand("kornia/kornia", 4201, model.StatusMerged, now.Add(-24*time.Hour))
	other.PRNumber = &n
	target := cand("kornia/tutorials", 7, model.StatusApproved)
	r := testCaps.Check([]*model.Candidate{other, target}, target, now)
	if len(r) != 1 || !strings.Contains(r[0], "cooldown until 2 Oct") ||
		!strings.Contains(r[0], "kornia/kornia#4455") {
		t.Fatalf("expected a dated cooldown naming the earlier PR, got %s", joined(r))
	}
}

func TestCapsCooldownIsPerOrgAndExpires(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	long := cand("kornia/kornia", 4201, model.StatusMerged, now.Add(-96*time.Hour))
	elsewhere := cand("helm/helm", 1, model.StatusMerged, now.Add(-1*time.Hour))
	target := cand("kornia/tutorials", 7, model.StatusApproved)
	if r := testCaps.Check([]*model.Candidate{long, elsewhere, target}, target, now); len(r) > 0 {
		t.Fatalf("expected no reasons, got %s", joined(r))
	}
}

func TestCapsCooldownIgnoresTheTargetsOwnHistory(t *testing.T) {
	// A pull request that was closed and is being reopened must not put its
	// own organisation into cooldown against itself.
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	target := cand("kornia/kornia", 4201, model.StatusApproved, now.Add(-24*time.Hour))
	if r := testCaps.Check([]*model.Candidate{target}, target, now); len(r) > 0 {
		t.Fatalf("expected no reasons, got %s", joined(r))
	}
}

func TestCapsReadsTheRealPolicyFile(t *testing.T) {
	cfg, err := Load(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Policy.Caps
	// The numbers the README states, asserted here so the two cannot drift.
	// They are deliberately loose -- ten pull requests a week, three open per
	// repository, no organisation cooldown -- and that is the operator's
	// decision rather than this package's default.
	if c.PRsPerDay != 4 || c.PRsPerWeek != 10 || c.MaxOpenPRs != 30 ||
		c.MaxOpenPerRepo != 3 || c.OrgCooldownDays != 0 {
		t.Fatalf("config/policy.yaml no longer holds the stated caps: %+v", c)
	}
	if c.MaxHarvestPerRun != 40 {
		t.Fatalf("max_harvest_per_run = %d, want 40", c.MaxHarvestPerRun)
	}
	// Not negotiable, and the reason is structural: with no approach and no
	// acceptance criteria there is no specification, so the implementer
	// refuses rather than inventing one. A candidate admitted by turning this
	// off could never be completed.
	if !cfg.Policy.Scoring.RequireApproachOrCriteria {
		t.Error("require_approach_or_criteria is off; such candidates cannot be implemented")
	}
	if !cfg.Policy.Scoring.RejectWorkflowChanges {
		t.Error("reject_workflow_changes is off, but preflight still refuses workflow diffs")
	}
}
