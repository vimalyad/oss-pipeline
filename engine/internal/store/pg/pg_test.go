package pg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vimalyad/oss-pipeline/engine/internal/model"
)

// These tests need a real Postgres, because every property worth asserting
// here is a property of the schema: the transition trigger, the append-only
// audit log, and the advisory lock inside claim_pr_slot. A fake would assert
// that the fake works.
//
// Skipped when OSSP_TEST_DATABASE_URL is unset so `go test ./...` stays
// green on a machine with no database.
func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("OSSP_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set OSSP_TEST_DATABASE_URL to run the store tests")
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	// Every test starts from nothing. TRUNCATE rather than DELETE so the
	// sequences restart and slugs in messages are predictable.
	if _, err := s.pool.Exec(ctx,
		`TRUNCATE candidates, repos, candidate_history, briefs, pr_signals,
		          audit, pull_requests, jobs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	return s
}

func cand(repo string, issue int, status model.Status) *model.Candidate {
	return &model.Candidate{
		Repo: repo, Issue: issue, Status: status,
		Title: "a broken thing", URL: fmt.Sprintf("https://github.com/%s/issues/%d", repo, issue),
		Labels:    []string{"bug", "good first issue"},
		Reactions: 3, Comments: 7,
		IssueCreatedAt: "2026-01-01T00:00:00Z",
		IssueUpdatedAt: "2026-09-01T00:00:00Z",
		History:        []model.HistoryEntry{{To: string(model.StatusDiscovered)}},
	}
}

func TestRoundTripKeepsEverything(t *testing.T) {
	s := testStore(t)
	c := cand("helm/helm", 13284, model.StatusProposed)
	c.Contest = model.ContestStalePR
	c.LinkedPRs = []int{32116, 32705}
	c.SoftPenalties = []string{"issue opened 3y ago"}
	c.Branch = "fix/issue-13284"
	c.TookOver = false
	c.Credits = "#32116 by @Zakharden"
	c.Brief = &model.Brief{
		MaintainerDesiredApproach: "Guard the nil case in loadDir.",
		ApproachAuthorAssociation: "MEMBER",
		ApproachSourceURL:         "https://github.com/helm/helm/issues/13284",
		AcceptanceCriteria:        []string{"a test covering the dangling link"},
		RejectedApproaches:        []string{"do not pre-filter before the walk"},
		ClaimedBy:                 "someone",
		ClaimedAt:                 "2026-02-01T00:00:00Z",
	}
	n := 150
	c.PRSignal = &model.PRSignal{
		Number: 32116, URL: "u", Author: "Zakharden",
		DaysSinceCommit: &n, Reviewed: true, Reasons: []string{"no commit for 150d"},
	}
	c.History = append(c.History,
		model.HistoryEntry{From: "discovered", To: "scored", Note: "cleared all bars"},
		model.HistoryEntry{From: "scored", To: "proposed", Note: "awaiting human approval"})

	if _, err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(c.Slug())
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name      string
		want, got any
	}{
		{"repo", c.Repo, got.Repo},
		{"issue", c.Issue, got.Issue},
		{"status", c.Status, got.Status},
		{"contest", c.Contest, got.Contest},
		{"branch", c.Branch, got.Branch},
		{"credits", c.Credits, got.Credits},
		{"linked prs", fmt.Sprint(c.LinkedPRs), fmt.Sprint(got.LinkedPRs)},
		{"labels", fmt.Sprint(c.Labels), fmt.Sprint(got.Labels)},
		{"soft penalties", fmt.Sprint(c.SoftPenalties), fmt.Sprint(got.SoftPenalties)},
		{"history length", len(c.History), len(got.History)},
	} {
		if fmt.Sprint(check.want) != fmt.Sprint(check.got) {
			t.Errorf("%s: got %v, want %v", check.name, check.got, check.want)
		}
	}
	if got.Brief == nil || got.Brief.ApproachAuthorAssociation != "MEMBER" ||
		len(got.Brief.AcceptanceCriteria) != 1 {
		t.Fatalf("brief did not round trip: %+v", got.Brief)
	}
	if got.PRSignal == nil || !got.PRSignal.Reviewed ||
		got.PRSignal.DaysSinceCommit == nil || *got.PRSignal.DaysSinceCommit != 150 {
		t.Fatalf("signal did not round trip: %+v", got.PRSignal)
	}
}

// The human gate, enforced by the database rather than by this engine. The
// predecessor checked the table in Go, which meant a second writer of the
// same state obeyed nothing at all.
func TestTheDatabaseRefusesTheHumanGate(t *testing.T) {
	s := testStore(t)
	c := cand("a/b", 1, model.StatusProposed)
	c.History = append(c.History,
		model.HistoryEntry{From: "discovered", To: "scored"},
		model.HistoryEntry{From: "scored", To: "proposed"})
	if _, err := s.Save(c); err != nil {
		t.Fatal(err)
	}

	c.Status = model.StatusImplementing
	c.History = append(c.History, model.HistoryEntry{From: "proposed", To: "implementing"})
	_, err := s.Save(c)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("err = %v, want an illegal transition", err)
	}
	// And nothing was written: the whole Save is one transaction.
	back, err := s.Load(c.Slug())
	if err != nil {
		t.Fatal(err)
	}
	if back.Status != model.StatusProposed || len(back.History) != 3 {
		t.Fatalf("a refused edge still changed state: %s with %d entries",
			back.Status, len(back.History))
	}
}

func TestForcedEdgeNeedsAnActor(t *testing.T) {
	s := testStore(t)
	c := cand("a/b", 1, model.StatusAbandoned)
	c.History = append(c.History,
		model.HistoryEntry{From: "discovered", To: "scored"},
		model.HistoryEntry{From: "scored", To: "proposed"},
		model.HistoryEntry{From: "proposed", To: "approved"},
		model.HistoryEntry{From: "approved", To: "abandoned"})
	if _, err := s.Save(c); err != nil {
		t.Fatal(err)
	}

	c.Status = model.StatusApproved
	c.History = append(c.History,
		model.HistoryEntry{From: "abandoned", To: "approved", Forced: true, Note: "retry"})
	if _, err := s.Save(c); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("a forced edge with no actor was accepted: %v", err)
	}

	c.History[len(c.History)-1].Actor = "human"
	if _, err := s.Save(c); err != nil {
		t.Fatalf("a properly attributed reopen was refused: %v", err)
	}
}

// Save appends history rather than replacing it, so saving twice must not
// duplicate anything. The predecessor rewrote the whole file every time,
// which is why a partial write could lose the lot.
func TestSavingTwiceDoesNotDuplicateHistory(t *testing.T) {
	s := testStore(t)
	c := cand("a/b", 1, model.StatusScored)
	c.History = append(c.History, model.HistoryEntry{From: "discovered", To: "scored"})
	for i := 0; i < 3; i++ {
		if _, err := s.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Load(c.Slug())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.History) != 2 {
		t.Fatalf("history has %d entries after three saves, want 2", len(got.History))
	}
}

func TestShouldReconsiderTreatsAbsenceAndSilenceDifferently(t *testing.T) {
	s := testStore(t)
	now := time.Now()

	if !s.ShouldReconsider("never-seen", now, 7) {
		t.Error("a slug with no row must be reconsidered")
	}

	live := cand("a/live", 1, model.StatusApproved)
	live.History = append(live.History,
		model.HistoryEntry{From: "discovered", To: "scored"},
		model.HistoryEntry{From: "scored", To: "proposed"},
		model.HistoryEntry{From: "proposed", To: "approved"})
	if _, err := s.Save(live); err != nil {
		t.Fatal(err)
	}
	if s.ShouldReconsider(live.Slug(), now, 7) {
		t.Error("live work must be left alone -- this is how sixteen approvals were destroyed")
	}

	for _, tc := range []struct {
		name, reason string
		want         bool
	}{
		{"human", "human rejection: not worth the review time", false},
		{"structural", "a/b is manually excluded", false},
		{"transient", "deferred: exceeded max_harvest=40 this run", true},
		{"quality", "no stated approach AND no acceptance criteria", true},
	} {
		c := cand("a/"+tc.name, 2, model.StatusRejected)
		c.RejectReason = tc.reason
		c.History = append(c.History,
			model.HistoryEntry{From: "discovered", To: "rejected", Note: tc.reason})
		if _, err := s.Save(c); err != nil {
			t.Fatal(err)
		}
		// Backdate past the window.
		if _, err := s.pool.Exec(context.Background(),
			`UPDATE candidates SET rejected_at = now() - INTERVAL '30 days' WHERE slug = $1`,
			c.Slug()); err != nil {
			t.Fatal(err)
		}
		if got := s.ShouldReconsider(c.Slug(), now, 7); got != tc.want {
			t.Errorf("%s rejection: reconsider = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAuditIsAppendOnly(t *testing.T) {
	s := testStore(t)
	if err := s.Record("approve", "a__b__1", "human approval"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `UPDATE audit SET detail = 'tampered'`); err == nil {
		t.Fatal("the audit log accepted an UPDATE")
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM audit`); err == nil {
		t.Fatal("the audit log accepted a DELETE")
	}
}

// TestConcurrentClaimsCannotBothWin is the reason this package exists.
//
// Two workers ask for the last slot at the same moment. Over a directory of
// JSON files they both read "one free" and both open a pull request; here the
// advisory lock inside claim_pr_slot serialises them, so exactly one gets it.
func TestConcurrentClaimsCannotBothWin(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// One slot left: the cap is 3 per repo and two are already open.
	if _, err := s.pool.Exec(ctx, `UPDATE caps SET max_open_per_repo = 3,
		max_open_prs = 100, prs_per_day = 100, prs_per_week = 100`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		open := cand("a/b", i, model.StatusPROpen)
		if _, err := s.Save(open); err != nil {
			t.Fatal(err)
		}
	}
	for _, issue := range []int{10, 11} {
		c := cand("a/b", issue, model.StatusApproved)
		c.History = append(c.History,
			model.HistoryEntry{From: "discovered", To: "scored"},
			model.HistoryEntry{From: "scored", To: "proposed"},
			model.HistoryEntry{From: "proposed", To: "approved"})
		if _, err := s.Save(c); err != nil {
			t.Fatal(err)
		}
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
	)
	for _, slug := range []string{"a__b__10", "a__b__11"} {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			tx, err := s.Begin(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			defer tx.Rollback(ctx)
			reasons, err := s.ClaimPRSlot(ctx, tx, slug)
			if err != nil {
				t.Error(err)
				return
			}
			if len(reasons) == 0 {
				// Hold the claim long enough that the other goroutine would
				// see a free slot if the lock were not doing its job.
				_, err = tx.Exec(ctx, `
					INSERT INTO candidate_history (candidate_id, from_status, to_status)
					SELECT id, 'approved', 'implementing' FROM candidates WHERE slug = $1`, slug)
				if err != nil {
					t.Error(err)
					return
				}
				_, err = tx.Exec(ctx, `
					INSERT INTO candidate_history (candidate_id, from_status, to_status)
					SELECT id, 'implementing', 'implemented' FROM candidates WHERE slug = $1`, slug)
				if err != nil {
					t.Error(err)
					return
				}
				_, err = tx.Exec(ctx, `
					INSERT INTO candidate_history (candidate_id, from_status, to_status)
					SELECT id, 'implemented', 'pushed' FROM candidates WHERE slug = $1`, slug)
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := tx.Exec(ctx,
					`UPDATE candidates SET status = 'pushed' WHERE slug = $1`, slug); err != nil {
					t.Error(err)
					return
				}
				if err := tx.Commit(ctx); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				winners = append(winners, slug)
				mu.Unlock()
			}
		}(slug)
	}
	wg.Wait()

	t.Logf("winners: %d %v", len(winners), winners)
	if len(winners) != 1 {
		t.Fatalf("%d of 2 claimants won the last slot, want exactly 1: %v",
			len(winners), winners)
	}
}

// migrationsDir is where the schema lives, found by walking up rather than by
// counting directory levels.
func migrationsDir(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		p := filepath.Join(dir, "db", "migrations")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no db/migrations above the working directory")
		}
		dir = parent
	}
}

// TestMigrationsApplyCleanly guards the thing a schema change breaks first.
func TestMigrationsApplyCleanly(t *testing.T) {
	if os.Getenv("OSSP_TEST_DATABASE_URL") == "" {
		t.Skip("set OSSP_TEST_DATABASE_URL to run the store tests")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql is not installed")
	}
	files, err := filepath.Glob(filepath.Join(migrationsDir(t), "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	t.Logf("%d migrations present", len(files))
}
