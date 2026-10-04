package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vimalyad/oss-pipeline/engine/internal/model"
)

// apiApprove is the dashboard's approval, written the way the API writes it:
// straight SQL, one transaction, no engine code involved. Kept verbatim here
// so a change to either side that breaks the other fails a test rather than a
// human decision.
func apiApprove(t *testing.T, s *Store, slug, actor string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var id int64
	if err := tx.QueryRow(ctx, `
		UPDATE candidates SET status = 'approved', updated_at = now()
		 WHERE slug = $1 AND status = 'proposed' RETURNING id`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO candidate_history (candidate_id, from_status, to_status, note, actor)
		VALUES ($1, 'proposed', 'approved', 'approved on the dashboard', $2)`, id, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit (action, slug, detail, actor) VALUES ('approve', $1, 'dashboard', $2)`,
		slug, actor); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func apiReject(t *testing.T, s *Store, slug, reason, actor string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var id int64
	if err := tx.QueryRow(ctx, `
		UPDATE candidates SET status = 'rejected', reject_kind = 'human',
		       reject_reason = $2, rejected_at = now(), updated_at = now()
		 WHERE slug = $1 AND status = 'proposed' RETURNING id`, slug, reason).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO candidate_history (candidate_id, from_status, to_status, note, actor)
		VALUES ($1, 'proposed', 'rejected', $2, $3)`, id, reason, actor); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func proposed(repo string, issue int) *model.Candidate {
	c := cand(repo, issue, model.StatusProposed)
	c.History = append(c.History,
		model.HistoryEntry{From: "discovered", To: "scored"},
		model.HistoryEntry{From: "scored", To: "proposed"})
	return c
}

// The failure this whole change is guarding against, with two services: a
// sweep loads a proposal, a person approves it on the dashboard, and the
// sweep's save puts the old status back. The predecessor lost sixteen
// decisions to the file-store version of exactly this.
func TestADashboardDecisionIsNotOverwrittenByAStaleCopy(t *testing.T) {
	s := testStore(t)
	c := proposed("a/b", 1)
	if _, err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	held, err := s.Load(c.Slug()) // the sweep's copy, taken before the decision
	if err != nil {
		t.Fatal(err)
	}

	apiApprove(t, s, c.Slug(), "vimal")

	// A save that changes nothing about status is still refused: it would
	// write status = proposed over the approval.
	held.Labels = append(held.Labels, "touched by a sweep")
	if _, err := s.Save(held); !errors.Is(err, ErrStale) {
		t.Fatalf("a stale copy was saved over a dashboard approval: %v", err)
	}
	// And so is one that tries to make its own decision.
	held.RejectReason = "scorer changed its mind"
	if err := model.Transition(held, model.StatusRejected, "rescored"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(held); !errors.Is(err, ErrStale) {
		t.Fatalf("a stale transition was saved over a dashboard approval: %v", err)
	}

	back, err := s.Load(c.Slug())
	if err != nil {
		t.Fatal(err)
	}
	if back.Status != model.StatusApproved {
		t.Fatalf("status is %s, want the dashboard's approval to stand", back.Status)
	}
	last := back.History[len(back.History)-1]
	if last.Actor != "vimal" || last.To != "approved" {
		t.Fatalf("last history entry is %+v, want the dashboard's", last)
	}

	// A fresh copy carries the approval and saves normally.
	if err := model.Transition(back, model.StatusImplementing, "picked up"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(back); err != nil {
		t.Fatalf("a fresh copy was refused: %v", err)
	}
}

// A person's rejection must stay a person's rejection however the dashboard
// phrased it, or the reconsider sweep brings it back in a week.
func TestADashboardRejectionStaysHumanAcrossEngineSaves(t *testing.T) {
	s := testStore(t)
	c := proposed("a/b", 2)
	if _, err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	apiReject(t, s, c.Slug(), "not a project I want to work on", "vimal")

	fresh, err := s.Load(c.Slug())
	if err != nil {
		t.Fatal(err)
	}
	fresh.Labels = append(fresh.Labels, "rescored")
	if _, err := s.Save(fresh); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT reject_kind FROM candidates WHERE slug = $1`, c.Slug()).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "human" {
		t.Fatalf("reject_kind became %q after an engine save", kind)
	}
	if s.ShouldReconsider(c.Slug(), time.Now().Add(365*24*time.Hour), 7) {
		t.Fatal("a human rejection came back for reconsideration")
	}
}

// The dashboard can make a scorer's rejection final without a history edge:
// the status stays rejected, only the kind and the reason change. An engine
// copy loaded before that is therefore not stale by the history check, and
// its save must not put the scorer's reason back over the person's.
func TestAHumanReasonSurvivesAnEngineSaveOfAMachineRejection(t *testing.T) {
	s := testStore(t)
	c := cand("a/b", 4, model.StatusRejected)
	c.History = append(c.History, model.HistoryEntry{From: "discovered", To: "rejected"})
	c.RejectReason = "no maintainer approach and no acceptance criteria"
	if _, err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	stale, err := s.Load(c.Slug())
	if err != nil {
		t.Fatal(err)
	}

	// What the API does for this case: no edge, kind and reason only.
	if _, err := s.pool.Exec(context.Background(), `
		UPDATE candidates SET reject_kind = 'human', reject_reason = 'human rejection: not for me',
		       rejected_at = now(), updated_at = now() WHERE slug = $1`, c.Slug()); err != nil {
		t.Fatal(err)
	}

	stale.Labels = append(stale.Labels, "rescored")
	if _, err := s.Save(stale); err != nil {
		t.Fatal(err)
	}
	var kind, reason string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT reject_kind, reject_reason FROM candidates WHERE slug = $1`, c.Slug()).Scan(&kind, &reason); err != nil {
		t.Fatal(err)
	}
	if kind != "human" || reason != "human rejection: not for me" {
		t.Fatalf("after an engine save: kind %q, reason %q", kind, reason)
	}
}

// Every field the engine needs on its next run. Dropping any of these does
// not fail loudly: the watcher re-reads every review as new, or forgets which
// pull request it is watching.
func TestEngineWorkingStateRoundTrips(t *testing.T) {
	s := testStore(t)
	c := cand("a/b", 3, model.StatusPROpen)
	n := 42
	c.PRNumber = &n
	c.PRURL = "https://github.com/a/b/pull/42"
	c.WatchSeen = []string{"R_1", "IC_2"}
	c.DisclosedAI = true
	c.QueuedReplies = []map[string]any{{"id": "R_3", "author": "maint", "body": "why?", "cls": "needs_reply"}}
	c.Facts = &model.RepoFacts{Repo: "a/b", Stars: 900, RequiredIssueLabels: []string{"accepted"},
		EligibilityQuote: "only accepted issues"}
	c.History = []model.HistoryEntry{{To: "pr_open"}}
	if _, err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(c.Slug())
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case got.PRNumber == nil || *got.PRNumber != 42:
		t.Errorf("pr number: %v", got.PRNumber)
	case got.PRURL != c.PRURL:
		t.Errorf("pr url: %q", got.PRURL)
	case len(got.WatchSeen) != 2:
		t.Errorf("watch seen: %v", got.WatchSeen)
	case !got.DisclosedAI:
		t.Error("disclosed_ai lost")
	case len(got.QueuedReplies) != 1 || got.QueuedReplies[0]["author"] != "maint":
		t.Errorf("queued replies: %v", got.QueuedReplies)
	case got.Facts == nil || got.Facts.EligibilityQuote != "only accepted issues" ||
		len(got.Facts.RequiredIssueLabels) != 1:
		t.Errorf("facts: %+v", got.Facts)
	}
}

func TestRepoFactsKeepEverythingAndTheirFetchTime(t *testing.T) {
	s := testStore(t)
	f := &model.RepoFacts{Repo: "a/b", Stars: 5, MergedFirstTimePR90d: true,
		ForbiddenIssueLabels: []string{"core-team"}, FetchedAt: "2026-01-02T03:04:05Z"}
	if err := s.SaveRepoFacts(f); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadRepoFacts("a/b")
	if err != nil {
		t.Fatal(err)
	}
	if !got.MergedFirstTimePR90d || len(got.ForbiddenIssueLabels) != 1 {
		t.Errorf("facts lost fields: %+v", got)
	}
	if got.FetchedAt != "2026-01-02T03:04:05Z" {
		t.Errorf("fetched_at = %s; an import must not make an old cache look fresh", got.FetchedAt)
	}
}

// What the dashboard reads: a candidate with a pull request has a row on the
// board, its queue shows up as feedback, and answering it clears the count.
func TestThePullRequestViewFollowsTheCandidate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := cand("a/b", 4, model.StatusChangesRequested)
	n := 9
	c.PRNumber = &n
	c.Branch = "fix/issue-4"
	c.QueuedReplies = []map[string]any{
		{"id": "R_1", "author": "maint", "kind": "review CHANGES_REQUESTED", "body": "rename it", "cls": "needs_reply"},
		{"author": "nobody", "body": "no id, cannot be deduplicated"},
	}
	c.History = []model.HistoryEntry{{To: "changes_requested"}}
	for i := 0; i < 2; i++ { // twice: nothing may duplicate
		if _, err := s.Save(c); err != nil {
			t.Fatal(err)
		}
	}

	var state, url string
	var unanswered, openFeedback int
	if err := s.pool.QueryRow(ctx, `
		SELECT state, pr_url, unanswered_items, open_feedback FROM v_pr_board WHERE number = 9`).
		Scan(&state, &url, &unanswered, &openFeedback); err != nil {
		t.Fatal(err)
	}
	if state != "open" || url != "https://github.com/a/b/pull/9" || unanswered != 1 || openFeedback != 1 {
		t.Fatalf("board row: state=%s url=%s unanswered=%d open=%d", state, url, unanswered, openFeedback)
	}
	var needs int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM v_needs_human WHERE what = 'reply'`).Scan(&needs); err != nil {
		t.Fatal(err)
	}
	if needs != 1 {
		t.Fatalf("needs-you shows %d replies, want 1", needs)
	}

	c.QueuedReplies[0]["draft"] = "Renamed in the latest push."
	c.QueuedReplies[0]["posted"] = true
	c.QueuedReplies[0]["posted_url"] = "https://github.com/a/b/pull/9#c1"
	if _, err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	var postedAt *time.Time
	if err := s.pool.QueryRow(ctx, `
		SELECT pr.unanswered_items, f.posted_at FROM pull_requests pr
		  JOIN feedback_items f ON f.pr_id = pr.id WHERE pr.number = 9`).
		Scan(&unanswered, &postedAt); err != nil {
		t.Fatal(err)
	}
	if unanswered != 0 || postedAt == nil {
		t.Fatalf("after posting: unanswered=%d posted_at=%v", unanswered, postedAt)
	}
}

func TestObservationsRecordOnlyStateChanges(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := cand("a/b", 5, model.StatusPROpen)
	n := 11
	c.PRNumber = &n
	c.History = []model.HistoryEntry{{To: "pr_open"}}
	if _, err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	add := 12
	for _, o := range []PRObservation{
		{Number: 11, URL: "u", Title: "Fix it", BaseBranch: "main", State: "ci_failing",
			ChecksTotal: 4, ChecksFailing: 1, Additions: &add},
		{Number: 11, URL: "u", Title: "Fix it", State: "ci_failing", ChecksTotal: 4, ChecksFailing: 1},
		{Number: 11, URL: "u", Title: "Fix it", State: "approved", ReviewDecision: "APPROVED",
			ChecksTotal: 4, RemoteUpdatedAt: "2026-10-01T00:00:00Z"},
	} {
		if err := s.ObservePR(c.Slug(), o); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT h.to_state::text FROM pr_state_history h
		  JOIN pull_requests pr ON pr.id = h.pr_id WHERE pr.number = 11 ORDER BY h.id`)
	if err != nil {
		t.Fatal(err)
	}
	var seq []string
	for rows.Next() {
		var st string
		if err := rows.Scan(&st); err != nil {
			t.Fatal(err)
		}
		seq = append(seq, st)
	}
	rows.Close()
	if want := "[open ci_failing approved]"; fmtList(seq) != want {
		t.Fatalf("history %v, want %s", seq, want)
	}
	var base string
	var additions *int
	if err := s.pool.QueryRow(ctx,
		`SELECT base_branch, additions FROM pull_requests WHERE number = 11`).Scan(&base, &additions); err != nil {
		t.Fatal(err)
	}
	// Later observations without a base or a diff size keep what was known.
	if base != "main" || additions == nil || *additions != 12 {
		t.Fatalf("base=%q additions=%v", base, additions)
	}
}

// An import must keep when things happened. now() for every row would make
// the timeline a single instant and restart every reconsider cooldown.
func TestImportedHistoryKeepsItsTimes(t *testing.T) {
	s := testStore(t)
	c := cand("a/b", 6, model.StatusRejected)
	c.RejectReason = "contest=claimed by someone"
	c.History = []model.HistoryEntry{
		{At: "2025-03-01T10:00:00+00:00", To: "discovered"},
		{At: "2025-03-01T10:05:00+00:00", From: "discovered", To: "rejected", Note: "claimed"},
	}
	if _, err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	var first, rejectedAt time.Time
	ctx := context.Background()
	if err := s.pool.QueryRow(ctx, `
		SELECT min(h.at), c.rejected_at FROM candidates c
		  JOIN candidate_history h ON h.candidate_id = c.id
		 WHERE c.slug = $1 GROUP BY c.rejected_at`, c.Slug()).Scan(&first, &rejectedAt); err != nil {
		t.Fatal(err)
	}
	if first.Year() != 2025 || rejectedAt.Year() != 2025 {
		t.Fatalf("first history at %v, rejected_at %v; want the original 2025 times", first, rejectedAt)
	}
	// Rerunning the import is a no-op.
	if _, err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	back, _ := s.Load(c.Slug())
	if len(back.History) != 2 {
		t.Fatalf("rerun left %d history entries", len(back.History))
	}
}

func fmtList(s []string) string {
	out := "["
	for i, x := range s {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out + "]"
}
