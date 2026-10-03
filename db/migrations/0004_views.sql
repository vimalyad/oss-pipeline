-- Read models for the dashboard. Views rather than API queries so both
-- services agree on what "open" and "needs you" mean.

BEGIN;

-- One row per pull request, with everything a card needs.
CREATE VIEW v_pr_board AS
SELECT pr.id,
       pr.number,
       r.full_name            AS repo,
       r.primary_language     AS language,
       r.stars,
       c.issue_number,
       c.title,
       c.url                  AS issue_url,
       pr.url                 AS pr_url,
       pr.state,
       pr.checks_total, pr.checks_failing, pr.checks_pending,
       pr.review_decision, pr.reviewer_count, pr.unanswered_items,
       pr.additions, pr.deletions, pr.changed_files,
       pr.opened_at, pr.remote_updated_at, pr.merged_at, pr.closed_at,
       EXTRACT(DAY FROM now() - COALESCE(pr.remote_updated_at, pr.opened_at))::INT AS idle_days,
       c.took_over, c.credits,
       (SELECT count(*) FROM feedback_items f WHERE f.pr_id = pr.id AND NOT f.posted) AS open_feedback
  FROM pull_requests pr
  JOIN candidates c ON c.id = pr.candidate_id
  JOIN repos r      ON r.id = pr.repo_id;

-- What is waiting on a person, in the order it should be dealt with. A reply
-- a maintainer is waiting for outranks a proposal nobody is waiting for.
CREATE VIEW v_needs_human AS
SELECT 'reply'::TEXT AS what, f.id AS ref_id, r.full_name AS repo, c.issue_number,
       c.title, pr.url AS link, f.author, f.created_at AS since, 1 AS rank
  FROM feedback_items f
  JOIN pull_requests pr ON pr.id = f.pr_id
  JOIN candidates c ON c.id = pr.candidate_id
  JOIN repos r ON r.id = pr.repo_id
 WHERE NOT f.posted
UNION ALL
SELECT 'blocker', c.id, r.full_name, c.issue_number, c.title, c.url,
       NULL, c.updated_at, 2
  FROM candidates c JOIN repos r ON r.id = c.repo_id
 WHERE cardinality(c.blockers) > 0 AND c.status IN ('approved','auto_approved')
UNION ALL
SELECT 'proposal', c.id, r.full_name, c.issue_number, c.title, c.url,
       NULL, c.updated_at, 3
  FROM candidates c JOIN repos r ON r.id = c.repo_id
 WHERE c.status = 'proposed';

-- The throughput question, answered from history rather than from a guess.
CREATE VIEW v_weekly_throughput AS
SELECT date_trunc('week', h.at)::DATE AS week,
       count(DISTINCT h.candidate_id) FILTER (WHERE h.to_status = 'pr_open')  AS opened,
       count(DISTINCT h.candidate_id) FILTER (WHERE h.to_status = 'merged')   AS merged,
       count(DISTINCT h.candidate_id) FILTER (WHERE h.to_status = 'closed')   AS closed,
       count(DISTINCT h.candidate_id) FILTER (WHERE h.to_status = 'proposed') AS proposed,
       count(DISTINCT h.candidate_id) FILTER (WHERE h.to_status IN ('approved','auto_approved')) AS approved
  FROM candidate_history h
 GROUP BY 1
 ORDER BY 1 DESC;

-- The funnel, which is what actually explains the throughput: the predecessor
-- tracked 405 candidates to produce 3 pull requests, and the loss was not
-- where anyone assumed.
CREATE VIEW v_funnel AS
SELECT to_status AS stage, count(DISTINCT candidate_id) AS reached
  FROM candidate_history
 GROUP BY 1;

COMMIT;
