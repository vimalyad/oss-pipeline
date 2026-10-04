package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/vimalyad/oss-pipeline/engine/internal/model"
)

// The pull_requests table is the dashboard's view of a pull request, and it
// has two writers inside this engine for two different reasons.
//
// Save keeps the row in existence: any candidate with a PR number has one,
// and its feedback queue is mirrored into feedback_items. That much is known
// without asking GitHub, so it is right even for a candidate imported from the
// file store that the watcher has not reached yet.
//
// ObservePR is the watcher reporting what GitHub said. It owns everything that
// only GitHub knows -- checks, review decision, diff size, the project's own
// updatedAt -- and the derived state, with a history row whenever that changes
// so "when did this go red" is answerable without re-reading GitHub.

// PRObservation is one watch cycle's reading of a pull request, already
// reduced to the schema's terms. State must be a pr_state value; deriving it
// belongs to the watcher, which is the package that understands what GitHub's
// fields mean.
type PRObservation struct {
	Number         int
	URL            string
	Title          string
	HeadBranch     string
	HeadSHA        string
	BaseBranch     string
	State          string
	ChecksTotal    int
	ChecksFailing  int
	ChecksPending  int
	ReviewDecision string
	ReviewerCount  int
	Additions      *int
	Deletions      *int
	ChangedFiles   *int
	// GitHub's timestamps, RFC 3339. Empty is unknown.
	RemoteUpdatedAt string
	MergedAt        string
	ClosedAt        string
	// Detail is the history row's note when the state changes.
	Detail string
}

// floorState is the pull request state a candidate's own status implies on its
// own. Only the terminal and in-flight statuses say anything; for the rest the
// watcher's reading is the only authority, and Save leaves the row alone.
func floorState(s model.Status) string {
	switch s {
	case model.StatusMerged:
		return "merged"
	case model.StatusClosed:
		return "closed"
	case model.StatusUpdating:
		return "updating"
	case model.StatusStale:
		return "stale"
	}
	return ""
}

// syncPR runs inside Save's transaction.
func syncPR(ctx context.Context, tx pgx.Tx, candID, repoID int64, c *model.Candidate) error {
	if c.PRNumber == nil {
		return nil
	}
	url := c.PRURL
	if url == "" {
		url = fmt.Sprintf("https://github.com/%s/pull/%d", c.Repo, *c.PRNumber)
	}
	var (
		prID     int64
		state    string
		inserted bool
	)
	// The issue title and an empty base branch are placeholders until the
	// first observation: both columns are NOT NULL, and a row that exists
	// with a provisional title is more use to the dashboard than no row.
	err := tx.QueryRow(ctx, `
		INSERT INTO pull_requests (candidate_id, repo_id, number, url, title,
		                           head_branch, base_branch)
		VALUES ($1,$2,$3,$4,$5,$6,'')
		ON CONFLICT (repo_id, number) DO UPDATE SET
		  candidate_id = EXCLUDED.candidate_id, url = EXCLUDED.url
		RETURNING id, state::text, (xmax = 0)`,
		candID, repoID, *c.PRNumber, url, c.Title, c.Branch).Scan(&prID, &state, &inserted)
	if err != nil {
		return fmt.Errorf("%w: pull request %s: %v", ErrStore, c.Slug(), err)
	}
	if inserted {
		if _, err := tx.Exec(ctx, `
			INSERT INTO pr_state_history (pr_id, from_state, to_state, detail)
			VALUES ($1, NULL, 'open', 'recorded')`, prID); err != nil {
			return fmt.Errorf("%w: pr history %s: %v", ErrStore, c.Slug(), err)
		}
		state = "open"
	}
	if floor := floorState(c.Status); floor != "" && floor != state {
		if err := setPRState(ctx, tx, prID, state, floor, "candidate is "+string(c.Status)); err != nil {
			return fmt.Errorf("%w: pr state %s: %v", ErrStore, c.Slug(), err)
		}
	}
	return syncFeedback(ctx, tx, prID, c)
}

func setPRState(ctx context.Context, tx pgx.Tx, prID int64, from, to, detail string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE pull_requests SET state = $2 WHERE id = $1`, prID, to); err != nil {
		return err
	}
	var fromArg *string
	if from != "" {
		fromArg = &from
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO pr_state_history (pr_id, from_state, to_state, detail)
		VALUES ($1, $2, $3, $4)`, prID, fromArg, to, nullStr(detail))
	return err
}

// syncFeedback mirrors the candidate's reply queue into feedback_items.
//
// The queue stays the engine's source of truth -- it is what `replies draft`
// and `replies post` read and mark -- and this table is its projection, keyed
// on GitHub's id so a re-save updates the same row instead of adding one.
// Items without an id cannot be deduplicated and are skipped rather than
// duplicated on every save.
func syncFeedback(ctx context.Context, tx pgx.Tx, prID int64, c *model.Candidate) error {
	for _, q := range c.QueuedReplies {
		ext := str(q, "id")
		if ext == "" {
			continue
		}
		author := str(q, "author")
		if author == "" {
			author = "?"
		}
		kind := str(q, "kind")
		if kind == "" {
			kind = "comment"
		}
		posted, _ := q["posted"].(bool)
		_, err := tx.Exec(ctx, `
			INSERT INTO feedback_items (pr_id, external_id, author, kind, body, url,
			  classification, created_at, draft, draft_rejected, posted, posted_url,
			  posted_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7, now(), $8,$9,$10,$11,
			        CASE WHEN $10 THEN now() END)
			ON CONFLICT (pr_id, external_id) DO UPDATE SET
			  classification = EXCLUDED.classification,
			  draft = EXCLUDED.draft, draft_rejected = EXCLUDED.draft_rejected,
			  posted = EXCLUDED.posted, posted_url = EXCLUDED.posted_url,
			  posted_at = CASE WHEN EXCLUDED.posted
			                   THEN coalesce(feedback_items.posted_at, now()) END`,
			prID, ext, author, kind, str(q, "body"), nullStr(str(q, "url")),
			nullStr(str(q, "cls")), nullStr(str(q, "draft")), arr(strs(q["draft_rejected"])),
			posted, nullStr(str(q, "posted_url")))
		if err != nil {
			return fmt.Errorf("%w: feedback %s %s: %v", ErrStore, c.Slug(), ext, err)
		}
	}
	_, err := tx.Exec(ctx, `
		UPDATE pull_requests SET unanswered_items =
		  (SELECT count(*) FROM feedback_items WHERE pr_id = $1 AND NOT posted)
		 WHERE id = $1`, prID)
	if err != nil {
		return fmt.Errorf("%w: unanswered %s: %v", ErrStore, c.Slug(), err)
	}
	return nil
}

// ObservePR records one watch cycle's reading of a candidate's pull request,
// and a state history row when the derived state moved.
func (s *Store) ObservePR(slug string, o PRObservation) error {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStore, err)
	}
	defer tx.Rollback(ctx)

	var candID, repoID int64
	var candTitle, branch string
	err = tx.QueryRow(ctx, `
		SELECT id, repo_id, title, coalesce(branch, '') FROM candidates WHERE slug = $1`,
		slug).Scan(&candID, &repoID, &candTitle, &branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	if err != nil {
		return fmt.Errorf("%w: observe %s: %v", ErrStore, slug, err)
	}
	title := o.Title
	if title == "" {
		title = candTitle
	}
	head := o.HeadBranch
	if head == "" {
		head = branch
	}

	var prior *string
	err = tx.QueryRow(ctx, `
		SELECT state::text FROM pull_requests WHERE repo_id = $1 AND number = $2
		   FOR UPDATE`, repoID, o.Number).Scan(&prior)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: observe %s: %v", ErrStore, slug, err)
	}

	var prID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO pull_requests (candidate_id, repo_id, number, url, title,
		  head_branch, head_sha, base_branch, state, checks_total, checks_failing,
		  checks_pending, review_decision, reviewer_count, additions, deletions,
		  changed_files, last_polled_at, remote_updated_at, merged_at, closed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,
		        now(), $18, $19, $20)
		ON CONFLICT (repo_id, number) DO UPDATE SET
		  candidate_id = EXCLUDED.candidate_id, url = EXCLUDED.url,
		  title = EXCLUDED.title, head_branch = EXCLUDED.head_branch,
		  head_sha = EXCLUDED.head_sha,
		  -- An observation that did not carry the base keeps the one we have.
		  base_branch = CASE WHEN EXCLUDED.base_branch = ''
		                     THEN pull_requests.base_branch ELSE EXCLUDED.base_branch END,
		  state = EXCLUDED.state, checks_total = EXCLUDED.checks_total,
		  checks_failing = EXCLUDED.checks_failing,
		  checks_pending = EXCLUDED.checks_pending,
		  review_decision = EXCLUDED.review_decision,
		  reviewer_count = EXCLUDED.reviewer_count,
		  additions = coalesce(EXCLUDED.additions, pull_requests.additions),
		  deletions = coalesce(EXCLUDED.deletions, pull_requests.deletions),
		  changed_files = coalesce(EXCLUDED.changed_files, pull_requests.changed_files),
		  last_polled_at = now(),
		  remote_updated_at = coalesce(EXCLUDED.remote_updated_at, pull_requests.remote_updated_at),
		  merged_at = coalesce(EXCLUDED.merged_at, pull_requests.merged_at),
		  closed_at = coalesce(EXCLUDED.closed_at, pull_requests.closed_at)
		RETURNING id`,
		candID, repoID, o.Number, o.URL, title, head, nullStr(o.HeadSHA), o.BaseBranch,
		o.State, o.ChecksTotal, o.ChecksFailing, o.ChecksPending, nullStr(o.ReviewDecision),
		o.ReviewerCount, o.Additions, o.Deletions, o.ChangedFiles,
		nullTime(o.RemoteUpdatedAt), nullTime(o.MergedAt), nullTime(o.ClosedAt)).Scan(&prID)
	if err != nil {
		return fmt.Errorf("%w: observe %s: %v", ErrStore, slug, err)
	}
	if prior == nil || *prior != o.State {
		var from *string
		if prior != nil {
			from = prior
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO pr_state_history (pr_id, from_state, to_state, detail)
			VALUES ($1, $2, $3, $4)`, prID, from, o.State, nullStr(o.Detail)); err != nil {
			return fmt.Errorf("%w: pr history %s: %v", ErrStore, slug, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: commit observe %s: %v", ErrStore, slug, err)
	}
	return nil
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// strs reads a string list out of a queued item, which after a JSON round
// trip is []any rather than []string.
func strs(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
