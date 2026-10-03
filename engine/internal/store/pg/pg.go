// Package pg stores candidates in Postgres.
//
// It replaces the JSON-file store, and the reason is narrower than "files are
// bad": two concurrent workers reading a directory both see a free pull
// request slot and both open one, and that cannot be fixed above the storage
// layer. Everything else here -- the state machine, the append-only audit --
// could have stayed in files, and the fact that it did not is why the rules
// now live in the schema where both this engine and the API must obey them.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vimalyad/oss-pipeline/engine/internal/model"
)

var (
	ErrStore    = errors.New("store")
	ErrNotFound = fmt.Errorf("%w: not found", ErrStore)
	// ErrIllegalTransition is the database refusing an edge. Surfaced as its
	// own error, and deliberately mapped onto the model's sentinel, so a
	// caller that already distinguishes "our ordering is wrong" from "the
	// network blipped" keeps working unchanged.
	ErrIllegalTransition = model.ErrIllegalTransition
)

type Store struct {
	pool *pgxpool.Pool
}

// Open connects and verifies the schema is present. A pool rather than a
// single connection because the worker loop runs several jobs at once.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	}
	cfg.MaxConns = 10
	cfg.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	}
	s := &Store{pool: pool}
	if err := s.ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() { s.pool.Close() }

// ping checks the connection and that migrations have run. A missing table is
// a far more useful error here than a missing column halfway through a sweep.
func (s *Store) ping(ctx context.Context) error {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		  WHERE table_schema = 'public'
		    AND table_name IN ('candidates','candidate_history','repos','caps','audit')`).Scan(&n)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStore, err)
	}
	if n != 5 {
		return fmt.Errorf("%w: schema is not applied (%d of 5 core tables present); "+
			"run db/migrations", ErrStore, n)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Repositories
// ---------------------------------------------------------------------------

// repoID returns the id for owner/name, inserting the row if it is new.
// Discovery meets repositories before anything knows anything else about them.
func (s *Store) repoID(ctx context.Context, tx pgx.Tx, full string) (int64, error) {
	owner, _, ok := strings.Cut(full, "/")
	if !ok {
		return 0, fmt.Errorf("%w: %q is not owner/name", ErrStore, full)
	}
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO repos (full_name, owner) VALUES ($1, $2)
		ON CONFLICT (full_name) DO UPDATE SET full_name = EXCLUDED.full_name
		RETURNING id`, full, owner).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("%w: repo %s: %v", ErrStore, full, err)
	}
	return id, nil
}

func (s *Store) LoadRepoFacts(repo string) (*model.RepoFacts, error) {
	ctx := context.Background()
	var f model.RepoFacts
	var fetched *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT full_name, coalesce(stars,0), coalesce(has_contributing,false),
		       coalesce(bans_ai_prs,false), coalesce(requires_ai_disclosure,false),
		       coalesce(ai_policy_quote,''), coalesce(requires_dco,false),
		       coalesce(requires_cla,false), coalesce(has_tests,false),
		       coalesce(primary_language,''), topics, facts_fetched_at
		  FROM repos WHERE full_name = $1 AND facts_fetched_at IS NOT NULL`, repo).Scan(
		&f.Repo, &f.Stars, &f.HasContributing, &f.BansAIPRs, &f.RequiresAIDisclosure,
		&f.AIPolicyQuote, &f.RequiresDCO, &f.RequiresCLA, &f.HasTests,
		&f.PrimaryLanguage, &f.Topics, &fetched)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: facts %s: %v", ErrStore, repo, err)
	}
	if fetched != nil {
		f.FetchedAt = fetched.UTC().Format(time.RFC3339)
	}
	return &f, nil
}

func (s *Store) SaveRepoFacts(f *model.RepoFacts) error {
	ctx := context.Background()
	owner, _, _ := strings.Cut(f.Repo, "/")
	_, err := s.pool.Exec(ctx, `
		INSERT INTO repos (full_name, owner, stars, has_contributing, bans_ai_prs,
		                   requires_ai_disclosure, ai_policy_quote, requires_dco,
		                   requires_cla, has_tests, primary_language, topics,
		                   facts_fetched_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12, now())
		ON CONFLICT (full_name) DO UPDATE SET
		  stars = EXCLUDED.stars,
		  has_contributing = EXCLUDED.has_contributing,
		  bans_ai_prs = EXCLUDED.bans_ai_prs,
		  requires_ai_disclosure = EXCLUDED.requires_ai_disclosure,
		  ai_policy_quote = EXCLUDED.ai_policy_quote,
		  requires_dco = EXCLUDED.requires_dco,
		  requires_cla = EXCLUDED.requires_cla,
		  has_tests = EXCLUDED.has_tests,
		  primary_language = EXCLUDED.primary_language,
		  topics = EXCLUDED.topics,
		  facts_fetched_at = now()`,
		f.Repo, owner, f.Stars, f.HasContributing, f.BansAIPRs, f.RequiresAIDisclosure,
		f.AIPolicyQuote, f.RequiresDCO, f.RequiresCLA, f.HasTests,
		f.PrimaryLanguage, arr(f.Topics))
	if err != nil {
		return fmt.Errorf("%w: save facts %s: %v", ErrStore, f.Repo, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Candidates
// ---------------------------------------------------------------------------

const candidateColumns = `
	c.id, r.full_name, c.issue_number, c.title, c.url, c.status,
	c.labels, c.comment_count, c.reaction_count,
	c.issue_created_at, c.issue_updated_at,
	c.contest, c.linked_prs,
	coalesce(c.reject_reason, ''), c.soft_penalties, c.blockers, c.score_failures,
	coalesce(c.branch, ''), c.took_over, coalesce(c.credits, '')`

func scanCandidate(row pgx.Row) (*model.Candidate, int64, error) {
	var (
		id               int64
		c                model.Candidate
		created, updated *time.Time
		contest          string
	)
	err := row.Scan(&id, &c.Repo, &c.Issue, &c.Title, &c.URL, &c.Status,
		&c.Labels, &c.Comments, &c.Reactions, &created, &updated,
		&contest, &c.LinkedPRs, &c.RejectReason, &c.SoftPenalties,
		&c.Blockers, &c.ScoreFailures, &c.Branch, &c.TookOver, &c.Credits)
	if err != nil {
		return nil, 0, err
	}
	c.Contest = model.Contest(contest)
	if c.Contest == "unknown" {
		// The schema needs a value; the model's absence is the empty string,
		// and score() distinguishes "not classified" from every real class.
		c.Contest = ""
	}
	if created != nil {
		c.IssueCreatedAt = created.UTC().Format(time.RFC3339)
	}
	if updated != nil {
		c.IssueUpdatedAt = updated.UTC().Format(time.RFC3339)
	}
	return &c, id, nil
}

func (s *Store) Load(slug string) (*model.Candidate, error) {
	ctx := context.Background()
	c, id, err := scanCandidate(s.pool.QueryRow(ctx,
		`SELECT`+candidateColumns+` FROM candidates c JOIN repos r ON r.id = c.repo_id
		  WHERE c.slug = $1`, slug))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: load %s: %v", ErrStore, slug, err)
	}
	if err := s.attach(ctx, id, c); err != nil {
		return nil, err
	}
	return c, nil
}

// attach fills in the parts kept in their own tables: the brief, the contest
// signal and the history. Separate queries rather than one join, because the
// brief has array columns and the history is one-to-many; a join would
// multiply the arrays by the row count.
func (s *Store) attach(ctx context.Context, id int64, c *model.Candidate) error {
	var b model.Brief
	var claimedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT coalesce(maintainer_approach,''), coalesce(approach_source_url,''),
		       coalesce(approach_association,''), rejected_approaches,
		       acceptance_criteria, open_questions, coalesce(claimed_by,''),
		       claimed_at, coalesce(reproduction,'')
		  FROM briefs WHERE candidate_id = $1`, id).Scan(
		&b.MaintainerDesiredApproach, &b.ApproachSourceURL, &b.ApproachAuthorAssociation,
		&b.RejectedApproaches, &b.AcceptanceCriteria, &b.OpenQuestions,
		&b.ClaimedBy, &claimedAt, &b.Reproduction)
	switch {
	case err == nil:
		if claimedAt != nil {
			b.ClaimedAt = claimedAt.UTC().Format(time.RFC3339)
		}
		c.Brief = &b
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%w: brief %s: %v", ErrStore, c.Slug(), err)
	}

	var sig model.PRSignal
	err = s.pool.QueryRow(ctx, `
		SELECT pr_number, coalesce(url,''), coalesce(author,''), is_draft,
		       days_since_commit, days_since_author_comment,
		       days_since_changes_requested, reviewed, has_stale_label,
		       checks_failing, reasons
		  FROM pr_signals WHERE candidate_id = $1`, id).Scan(
		&sig.Number, &sig.URL, &sig.Author, &sig.IsDraft,
		&sig.DaysSinceCommit, &sig.DaysSinceAuthorComment,
		&sig.DaysSinceChangesReqested, &sig.Reviewed, &sig.HasStaleLabel,
		&sig.ChecksFailing, &sig.Reasons)
	switch {
	case err == nil:
		c.PRSignal = &sig
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%w: signal %s: %v", ErrStore, c.Slug(), err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT from_status, to_status, coalesce(note,''), forced, coalesce(actor,''), at
		  FROM candidate_history WHERE candidate_id = $1 ORDER BY id`, id)
	if err != nil {
		return fmt.Errorf("%w: history %s: %v", ErrStore, c.Slug(), err)
	}
	defer rows.Close()
	for rows.Next() {
		var h model.HistoryEntry
		var from *string
		var at time.Time
		if err := rows.Scan(&from, &h.To, &h.Note, &h.Forced, &h.Actor, &at); err != nil {
			return fmt.Errorf("%w: history %s: %v", ErrStore, c.Slug(), err)
		}
		if from != nil {
			h.From = *from
		}
		h.At = at.UTC().Format(time.RFC3339)
		c.History = append(c.History, h)
	}
	return rows.Err()
}

// Save writes the candidate and any history entries not yet persisted.
//
// One transaction, and the history is appended rather than replaced: the
// database trigger validates every edge, so an ordering bug in this engine
// fails here instead of being written down. That is the inversion worth
// having -- the predecessor checked the state machine in application code,
// which meant a second writer of the same files obeyed nothing at all.
func (s *Store) Save(c *model.Candidate) (string, error) {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrStore, err)
	}
	defer tx.Rollback(ctx)

	repoID, err := s.repoID(ctx, tx, c.Repo)
	if err != nil {
		return "", err
	}
	contest := string(c.Contest)
	if contest == "" {
		contest = "unknown"
	}
	kind, reason := rejection(c)

	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO candidates (repo_id, issue_number, slug, title, url, status,
		  labels, comment_count, reaction_count, issue_created_at, issue_updated_at,
		  contest, linked_prs, reject_kind, reject_reason, rejected_at,
		  soft_penalties, blockers, score_failures, branch, took_over, credits)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
		        CASE WHEN $14::rejection_kind IS NULL THEN NULL ELSE now() END,
		        $16,$17,$18,$19,$20,$21)
		ON CONFLICT (slug) DO UPDATE SET
		  title = EXCLUDED.title, url = EXCLUDED.url, status = EXCLUDED.status,
		  labels = EXCLUDED.labels, comment_count = EXCLUDED.comment_count,
		  reaction_count = EXCLUDED.reaction_count,
		  issue_updated_at = EXCLUDED.issue_updated_at,
		  contest = EXCLUDED.contest, linked_prs = EXCLUDED.linked_prs,
		  reject_kind = EXCLUDED.reject_kind, reject_reason = EXCLUDED.reject_reason,
		  rejected_at = coalesce(candidates.rejected_at, EXCLUDED.rejected_at),
		  soft_penalties = EXCLUDED.soft_penalties, blockers = EXCLUDED.blockers,
		  score_failures = EXCLUDED.score_failures, branch = EXCLUDED.branch,
		  took_over = EXCLUDED.took_over, credits = EXCLUDED.credits,
		  updated_at = now()
		RETURNING id`,
		repoID, c.Issue, c.Slug(), c.Title, c.URL, string(c.Status),
		arr(c.Labels), c.Comments, c.Reactions, nullTime(c.IssueCreatedAt), nullTime(c.IssueUpdatedAt),
		contest, arr(c.LinkedPRs), kind, nullStr(reason),
		arr(c.SoftPenalties), arr(c.Blockers), arr(c.ScoreFailures), nullStr(c.Branch),
		c.TookOver, nullStr(c.Credits)).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("%w: save %s: %v", ErrStore, c.Slug(), err)
	}

	if err := saveBrief(ctx, tx, id, c); err != nil {
		return "", err
	}
	if err := saveSignal(ctx, tx, id, c); err != nil {
		return "", err
	}

	// Append only the entries the database does not have. History is
	// append-only by construction, so a count is a safe watermark.
	var have int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM candidate_history WHERE candidate_id = $1`, id).Scan(&have); err != nil {
		return "", fmt.Errorf("%w: %v", ErrStore, err)
	}
	for _, h := range c.History[min(have, len(c.History)):] {
		_, err := tx.Exec(ctx, `
			INSERT INTO candidate_history (candidate_id, from_status, to_status,
			                               note, forced, actor, at)
			VALUES ($1,$2,$3,$4,$5,$6, coalesce($7, now()))`,
			id, nullStr(h.From), h.To, nullStr(h.Note), h.Forced,
			nullStr(h.Actor), nullTime(h.At))
		if err != nil {
			// The trigger speaks for itself; wrap it so callers that already
			// branch on an illegal transition keep working.
			if strings.Contains(err.Error(), "illegal transition") ||
				strings.Contains(err.Error(), "sanctioned reopen") ||
				strings.Contains(err.Error(), "must record who forced it") {
				return "", fmt.Errorf("%w: %s: %v", ErrIllegalTransition, c.Slug(), err)
			}
			return "", fmt.Errorf("%w: history %s: %v", ErrStore, c.Slug(), err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("%w: commit %s: %v", ErrStore, c.Slug(), err)
	}
	return c.Slug(), nil
}

func saveBrief(ctx context.Context, tx pgx.Tx, id int64, c *model.Candidate) error {
	if c.Brief == nil {
		return nil
	}
	b := c.Brief
	_, err := tx.Exec(ctx, `
		INSERT INTO briefs (candidate_id, maintainer_approach, approach_source_url,
		  approach_association, rejected_approaches, acceptance_criteria,
		  open_questions, claimed_by, claimed_at, reproduction)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (candidate_id) DO UPDATE SET
		  maintainer_approach = EXCLUDED.maintainer_approach,
		  approach_source_url = EXCLUDED.approach_source_url,
		  approach_association = EXCLUDED.approach_association,
		  rejected_approaches = EXCLUDED.rejected_approaches,
		  acceptance_criteria = EXCLUDED.acceptance_criteria,
		  open_questions = EXCLUDED.open_questions,
		  claimed_by = EXCLUDED.claimed_by, claimed_at = EXCLUDED.claimed_at,
		  reproduction = EXCLUDED.reproduction, extracted_at = now()`,
		id, nullStr(b.MaintainerDesiredApproach), nullStr(b.ApproachSourceURL),
		nullStr(b.ApproachAuthorAssociation), arr(b.RejectedApproaches),
		arr(b.AcceptanceCriteria), arr(b.OpenQuestions), nullStr(b.ClaimedBy),
		nullTime(b.ClaimedAt), nullStr(b.Reproduction))
	if err != nil {
		return fmt.Errorf("%w: brief %s: %v", ErrStore, c.Slug(), err)
	}
	return nil
}

func saveSignal(ctx context.Context, tx pgx.Tx, id int64, c *model.Candidate) error {
	if c.PRSignal == nil {
		return nil
	}
	g := c.PRSignal
	_, err := tx.Exec(ctx, `
		INSERT INTO pr_signals (candidate_id, pr_number, url, author, is_draft,
		  days_since_commit, days_since_author_comment, days_since_changes_requested,
		  reviewed, has_stale_label, checks_failing, reasons)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (candidate_id) DO UPDATE SET
		  pr_number = EXCLUDED.pr_number, url = EXCLUDED.url, author = EXCLUDED.author,
		  is_draft = EXCLUDED.is_draft,
		  days_since_commit = EXCLUDED.days_since_commit,
		  days_since_author_comment = EXCLUDED.days_since_author_comment,
		  days_since_changes_requested = EXCLUDED.days_since_changes_requested,
		  reviewed = EXCLUDED.reviewed, has_stale_label = EXCLUDED.has_stale_label,
		  checks_failing = EXCLUDED.checks_failing, reasons = EXCLUDED.reasons,
		  observed_at = now()`,
		id, g.Number, nullStr(g.URL), nullStr(g.Author), g.IsDraft,
		g.DaysSinceCommit, g.DaysSinceAuthorComment, g.DaysSinceChangesReqested,
		g.Reviewed, g.HasStaleLabel, g.ChecksFailing, arr(g.Reasons))
	if err != nil {
		return fmt.Errorf("%w: signal %s: %v", ErrStore, c.Slug(), err)
	}
	return nil
}

// All returns every candidate. The second return value exists for parity with
// the file store, which could not read a hand-corrupted JSON file; here a row
// either scans or the query fails, so it is always empty and that is the
// point -- the class of error it reported cannot happen any more.
func (s *Store) All() ([]*model.Candidate, []LoadResult) {
	ctx := context.Background()
	cs, err := s.query(ctx, `SELECT`+candidateColumns+`
		  FROM candidates c JOIN repos r ON r.id = c.repo_id ORDER BY c.slug`)
	if err != nil {
		return nil, []LoadResult{{Slug: "", Err: err}}
	}
	return cs, nil
}

// LoadResult reports a candidate that could not be read.
type LoadResult struct {
	Slug string
	Err  error
}

func (s *Store) ByStatus(want ...model.Status) []*model.Candidate {
	if len(want) == 0 {
		return nil
	}
	ss := make([]string, len(want))
	for i, w := range want {
		ss[i] = string(w)
	}
	ctx := context.Background()
	cs, err := s.query(ctx, `SELECT`+candidateColumns+`
		  FROM candidates c JOIN repos r ON r.id = c.repo_id
		 WHERE c.status = ANY($1) ORDER BY c.slug`, ss)
	if err != nil {
		return nil
	}
	return cs
}

func (s *Store) query(ctx context.Context, sql string, args ...any) ([]*model.Candidate, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	}
	type pair struct {
		c  *model.Candidate
		id int64
	}
	var found []pair
	for rows.Next() {
		c, id, err := scanCandidate(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("%w: %v", ErrStore, err)
		}
		found = append(found, pair{c, id})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	}
	out := make([]*model.Candidate, 0, len(found))
	for _, p := range found {
		if err := s.attach(ctx, p.id, p.c); err != nil {
			return nil, err
		}
		out = append(out, p.c)
	}
	return out, nil
}

// ShouldReconsider reports whether a sweep may look at this slug again.
//
// Unknown means never seen, which is the one safe reading of absence. A row
// that exists but is not rejected is live work and must be left alone --
// saying otherwise is how the predecessor destroyed sixteen human decisions,
// and the reason the query asks the database rather than parsing a file is
// that a row cannot half-exist.
func (s *Store) ShouldReconsider(slug string, now time.Time, afterDays int) bool {
	ctx := context.Background()
	var (
		status     string
		kind       *string
		rejectedAt *time.Time
	)
	err := s.pool.QueryRow(ctx,
		`SELECT status, reject_kind, rejected_at FROM candidates WHERE slug = $1`,
		slug).Scan(&status, &kind, &rejectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	if err != nil {
		// A read failure is not evidence of absence. Leaving it alone costs
		// one missed candidate; the other reading costs whatever was on disk.
		return false
	}
	if model.Status(status) != model.StatusRejected {
		return false
	}
	switch {
	case kind == nil:
		return false
	case *kind == "human", *kind == "structural":
		return false
	}
	if rejectedAt == nil {
		return false
	}
	return now.Sub(*rejectedAt) >= time.Duration(afterDays)*24*time.Hour
}

// ---------------------------------------------------------------------------
// Caps
// ---------------------------------------------------------------------------

// ClaimPRSlot asks the database whether this candidate may become a pull
// request, and holds the answer for the rest of the transaction.
//
// This is the method the whole storage change exists for. Two workers calling
// it concurrently serialise on an advisory lock inside the function, so the
// count one of them reads cannot change underneath it. The file store could
// not express that at any cost.
//
// Call it in the same transaction that records the push, or the lock is
// released before the slot is used and the guarantee is gone.
func (s *Store) ClaimPRSlot(ctx context.Context, tx pgx.Tx, slug string) ([]string, error) {
	var reasons []string
	err := tx.QueryRow(ctx,
		`SELECT claim_pr_slot((SELECT id FROM candidates WHERE slug = $1))`,
		slug).Scan(&reasons)
	if err != nil {
		return nil, fmt.Errorf("%w: claim %s: %v", ErrStore, slug, err)
	}
	return reasons, nil
}

// Begin exposes a transaction so a caller can hold a claim across its own
// writes. Named rather than hidden, because the window in which the claim is
// valid is exactly the transaction and a caller has to be able to see it.
func (s *Store) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	}
	return tx, nil
}

// Record appends to the audit log. The table refuses UPDATE and DELETE, so
// this is the only thing that can happen to it.
func (s *Store) Record(action, slug, detail string) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO audit (action, slug, detail) VALUES ($1,$2,$3)`,
		action, nullStr(slug), nullStr(detail))
	if err != nil {
		return fmt.Errorf("%w: audit: %v", ErrStore, err)
	}
	return nil
}
