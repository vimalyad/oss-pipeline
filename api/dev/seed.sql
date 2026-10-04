-- Fictional data for working on the dashboard locally. Never load this into
-- the real database: every repository, issue and pull request here is made up.
--
-- History is written edge by edge through the real trigger, so the seed itself
-- is checked against allowed_transitions -- a seed that needed an illegal edge
-- would be describing a state the engine can never reach.

BEGIN;

INSERT INTO repos (full_name, owner, primary_language, stars, domain) VALUES
    ('example-org/tensorkit',  'example-org', 'Python', 18200, 'ml'),
    ('example-org/gridview',   'example-org', 'TypeScript', 9400, 'web'),
    ('sample-labs/fastcsv',    'sample-labs', 'Rust', 4100, 'data'),
    ('sample-labs/queuectl',   'sample-labs', 'Go', 2700, 'infra'),
    ('demo-project/docforge',  'demo-project', 'Go', 12800, 'tooling');

CREATE FUNCTION pg_temp.walk(p_slug TEXT, p_repo TEXT, p_issue INT, p_title TEXT,
                             p_path candidate_status[], p_start TIMESTAMPTZ, p_step INTERVAL,
                             p_labels TEXT[] DEFAULT '{good first issue}',
                             p_reject_kind rejection_kind DEFAULT NULL, p_reject_reason TEXT DEFAULT NULL)
RETURNS BIGINT AS $$
DECLARE
    cid BIGINT;
    i   INT;
    t   TIMESTAMPTZ := p_start;
BEGIN
    INSERT INTO candidates (repo_id, issue_number, slug, title, url, status, labels,
                            issue_created_at, contest, created_at, updated_at)
    SELECT r.id, p_issue, p_slug, p_title,
           'https://github.com/' || p_repo || '/issues/' || p_issue,
           p_path[1], p_labels, p_start - INTERVAL '40 days', 'no_pr', p_start, p_start
      FROM repos r WHERE r.full_name = p_repo
    RETURNING id INTO cid;
    INSERT INTO candidate_history (candidate_id, from_status, to_status, note, at)
    VALUES (cid, NULL, p_path[1], 'found by discovery', t);
    FOR i IN 2 .. array_length(p_path, 1) LOOP
        t := t + p_step;
        INSERT INTO candidate_history (candidate_id, from_status, to_status, note, actor, at)
        VALUES (cid, p_path[i-1], p_path[i],
                CASE p_path[i] WHEN 'approved' THEN 'human approval'
                               WHEN 'scored' THEN 'cleared every bar'
                               WHEN 'pr_open' THEN 'pull request opened'
                               ELSE NULL END,
                CASE p_path[i] WHEN 'approved' THEN 'vimalyad' ELSE NULL END, t);
    END LOOP;
    -- One statement, because rejection_is_explained refuses a rejected row
    -- that does not yet say why.
    UPDATE candidates SET status = p_path[array_length(p_path, 1)], updated_at = t,
           reject_kind = p_reject_kind, reject_reason = p_reject_reason,
           rejected_at = CASE WHEN p_reject_kind IS NULL THEN NULL ELSE t END
     WHERE id = cid;
    RETURN cid;
END;
$$ LANGUAGE plpgsql;

CREATE FUNCTION pg_temp.pr(p_slug TEXT, p_number INT, p_states pr_state[], p_start TIMESTAMPTZ,
                           p_step INTERVAL, p_failing INT DEFAULT 0, p_pending INT DEFAULT 0,
                           p_review TEXT DEFAULT NULL)
RETURNS BIGINT AS $$
DECLARE
    pid BIGINT;
    i   INT;
    t   TIMESTAMPTZ := p_start;
    final pr_state := p_states[array_length(p_states, 1)];
BEGIN
    INSERT INTO pull_requests (candidate_id, repo_id, number, url, title, head_branch, base_branch,
                               state, checks_total, checks_failing, checks_pending, review_decision,
                               reviewer_count, additions, deletions, changed_files, opened_at,
                               last_polled_at, remote_updated_at, merged_at, closed_at)
    SELECT c.id, c.repo_id, p_number,
           'https://github.com/' || r.full_name || '/pull/' || p_number,
           c.title, 'fix/' || c.issue_number, 'main', final,
           12, p_failing, p_pending, p_review, CASE WHEN p_review IS NULL THEN 0 ELSE 1 END,
           40 + p_number % 90, 5 + p_number % 30, 1 + p_number % 5, p_start, now(),
           p_start + p_step * (array_length(p_states, 1) - 1),
           CASE WHEN final = 'merged' THEN p_start + p_step * (array_length(p_states, 1) - 1) END,
           CASE WHEN final = 'closed' THEN p_start + p_step * (array_length(p_states, 1) - 1) END
      FROM candidates c JOIN repos r ON r.id = c.repo_id WHERE c.slug = p_slug
    RETURNING id INTO pid;
    INSERT INTO pr_state_history (pr_id, from_state, to_state, detail, at)
    VALUES (pid, NULL, p_states[1], 'opened', t);
    FOR i IN 2 .. array_length(p_states, 1) LOOP
        t := t + p_step;
        INSERT INTO pr_state_history (pr_id, from_state, to_state, detail, at)
        VALUES (pid, p_states[i-1], p_states[i],
                CASE p_states[i] WHEN 'ci_failing' THEN 'test (ubuntu-latest, 3.12) failed'
                                 WHEN 'changes_requested' THEN 'maintainer requested changes'
                                 WHEN 'updating' THEN 'pushing a fix'
                                 WHEN 'approved' THEN 'approved by a maintainer'
                                 ELSE NULL END, t);
    END LOOP;
    RETURN pid;
END;
$$ LANGUAGE plpgsql;

-- Merged, a fortnight ago and last week.
SELECT pg_temp.walk('example-org__tensorkit__2210', 'example-org/tensorkit', 2210,
    'resize() ignores antialias=False for uint8 tensors',
    '{discovered,scored,proposed,approved,implementing,implemented,pushed,pr_open,merged}',
    now() - INTERVAL '20 days', INTERVAL '14 hours');
SELECT pg_temp.pr('example-org__tensorkit__2210', 2291,
    '{open,ci_pending,under_review,approved,merged}', now() - INTERVAL '17 days', INTERVAL '20 hours',
    0, 0, 'APPROVED');

SELECT pg_temp.walk('sample-labs__fastcsv__88', 'sample-labs/fastcsv', 88,
    'Quoted header with embedded newline drops the next column',
    '{discovered,scored,proposed,approved,implementing,implemented,pushed,pr_open,changes_requested,updating,pr_open,merged}',
    now() - INTERVAL '12 days', INTERVAL '18 hours');
SELECT pg_temp.pr('sample-labs__fastcsv__88', 412,
    '{open,under_review,changes_requested,updating,ci_pending,approved,merged}',
    now() - INTERVAL '8 days', INTERVAL '12 hours', 0, 0, 'APPROVED');

-- Open, in every condition the board must tell apart.
SELECT pg_temp.walk('example-org__gridview__1532', 'example-org/gridview', 1532,
    'Column resize handle is unreachable by keyboard',
    '{discovered,scored,proposed,approved,implementing,implemented,pushed,pr_open,changes_requested}',
    now() - INTERVAL '6 days', INTERVAL '10 hours');
SELECT pg_temp.pr('example-org__gridview__1532', 1601,
    '{open,ci_pending,under_review,changes_requested}', now() - INTERVAL '4 days', INTERVAL '16 hours',
    0, 0, 'CHANGES_REQUESTED');

SELECT pg_temp.walk('sample-labs__queuectl__301', 'sample-labs/queuectl', 301,
    'Retry backoff overflows after 63 attempts',
    '{discovered,scored,proposed,approved,implementing,implemented,pushed,pr_open}',
    now() - INTERVAL '3 days', INTERVAL '6 hours');
SELECT pg_temp.pr('sample-labs__queuectl__301', 318,
    '{open,ci_pending,ci_failing}', now() - INTERVAL '30 hours', INTERVAL '3 hours', 2, 0);

SELECT pg_temp.walk('demo-project__docforge__977', 'demo-project/docforge', 977,
    'Anchor links break when a heading contains backticks',
    '{discovered,scored,proposed,approved,implementing,implemented,pushed,pr_open}',
    now() - INTERVAL '30 hours', INTERVAL '3 hours');
SELECT pg_temp.pr('demo-project__docforge__977', 1004,
    '{open,ci_pending}', now() - INTERVAL '6 hours', INTERVAL '2 hours', 0, 5);

SELECT pg_temp.walk('example-org__tensorkit__2304', 'example-org/tensorkit', 2304,
    'normalize() divides by zero on a constant channel',
    '{discovered,scored,proposed,approved,implementing,implemented,pushed,pr_open}',
    now() - INTERVAL '50 days', INTERVAL '8 hours');
SELECT pg_temp.pr('example-org__tensorkit__2304', 2318,
    '{open,review_required,stale}', now() - INTERVAL '47 days', INTERVAL '10 days');

-- Closed without merging.
SELECT pg_temp.walk('example-org__gridview__1490', 'example-org/gridview', 1490,
    'Sticky header flickers on Safari when scrolling fast',
    '{discovered,scored,proposed,approved,implementing,implemented,pushed,pr_open,closed}',
    now() - INTERVAL '25 days', INTERVAL '20 hours');
SELECT pg_temp.pr('example-org__gridview__1490', 1512,
    '{open,under_review,closed}', now() - INTERVAL '21 days', INTERVAL '3 days');

-- Waiting on you.
SELECT pg_temp.walk('sample-labs__fastcsv__102', 'sample-labs/fastcsv', 102,
    'Reader panics on a BOM followed by an empty file',
    '{discovered,scored,proposed}', now() - INTERVAL '20 hours', INTERVAL '2 hours',
    '{bug,help wanted}');
SELECT pg_temp.walk('demo-project__docforge__1012', 'demo-project/docforge', 1012,
    'Table of contents skips headings nested in admonitions',
    '{discovered,scored,proposed}', now() - INTERVAL '9 hours', INTERVAL '1 hour');
SELECT pg_temp.walk('sample-labs__queuectl__322', 'sample-labs/queuectl', 322,
    'queuectl drain exits 0 when a worker is still running',
    '{discovered,scored,proposed}', now() - INTERVAL '4 hours', INTERVAL '30 minutes',
    '{good first issue,bug/confirmed}');

-- Approved but blocked, and in flight.
SELECT pg_temp.walk('example-org__tensorkit__2350', 'example-org/tensorkit', 2350,
    'Metal backend returns NaN for grid_sample with border padding',
    '{discovered,scored,proposed,approved}', now() - INTERVAL '2 days', INTERVAL '5 hours');
UPDATE candidates SET blockers = '{needs an Apple GPU to verify; this machine has none}'
 WHERE slug = 'example-org__tensorkit__2350';
SELECT pg_temp.walk('example-org__gridview__1544', 'example-org/gridview', 1544,
    'aria-sort is never set on the active column',
    '{discovered,scored,proposed,approved,implementing}', now() - INTERVAL '5 hours', INTERVAL '1 hour');

-- Rejected: one by the scorer, one by you.
SELECT pg_temp.walk('sample-labs__queuectl__290', 'sample-labs/queuectl', 290,
    'Rewrite the scheduler around a priority heap',
    '{discovered,scored,rejected}', now() - INTERVAL '9 days', INTERVAL '1 hour',
    '{help wanted}', 'quality', 'no maintainer approach and no acceptance criteria');
UPDATE candidates SET score_failures = '{require_approach_or_criteria}'
 WHERE slug = 'sample-labs__queuectl__290';
SELECT pg_temp.walk('demo-project__docforge__950', 'demo-project/docforge', 950,
    'Typo in the CLI help for --out-dir',
    '{discovered,scored,proposed,rejected}', now() - INTERVAL '11 days', INTERVAL '3 hours',
    '{good first issue}', 'human', 'human rejection: docs typo, not worth a maintainer''s review');

-- A brief and a contest signal for the proposals, which is what the decision rests on.
INSERT INTO briefs (candidate_id, maintainer_approach, approach_source_url, approach_association,
                    rejected_approaches, acceptance_criteria, open_questions, reproduction)
SELECT id, 'Skip the BOM before sniffing the header, and return an empty reader rather than an error.',
       url || '#issuecomment-1', 'MEMBER',
       '{Stripping the BOM in the CLI only -- the library is what is broken}',
       '{An input of only a UTF-8 BOM yields zero records and no error,A BOM before a real header is not part of the first field name}',
       '{Should a UTF-16 BOM be rejected explicitly?}',
       'printf ''\xEF\xBB\xBF'' > e.csv && fastcsv count e.csv'
  FROM candidates WHERE slug = 'sample-labs__fastcsv__102';
INSERT INTO briefs (candidate_id, acceptance_criteria, dropped)
SELECT id, '{Headings inside !!! note blocks appear in the TOC at their nesting depth}',
       '{maintainer_approach: quoted comment was by a non-member (CONTRIBUTOR)}'
  FROM candidates WHERE slug = 'demo-project__docforge__1012';
INSERT INTO briefs (candidate_id, maintainer_approach, approach_association, acceptance_criteria)
SELECT id, 'drain should wait on the worker group and exit 3 on timeout.', 'OWNER',
       '{drain exits non-zero while any worker is running,Exit code 3 is documented}'
  FROM candidates WHERE slug = 'sample-labs__queuectl__322';
INSERT INTO pr_signals (candidate_id, pr_number, url, author, days_since_commit,
                        days_since_author_comment, reviewed, reasons)
SELECT id, 97, 'https://github.com/sample-labs/fastcsv/pull/97', 'someone-else', 210, 190, FALSE,
       '{author silent 190 days,no commits in 210 days,never reviewed -- backlog rather than abandoned}'
  FROM candidates WHERE slug = 'sample-labs__fastcsv__102';
UPDATE candidates SET contest = 'stale_pr', linked_prs = '{97}',
       soft_penalties = '{no CONTRIBUTING.md}'
 WHERE slug = 'sample-labs__fastcsv__102';

-- Maintainer feedback: one answered, one waiting on you.
INSERT INTO feedback_items (pr_id, external_id, author, author_association, kind, body, url,
                            classification, created_at, draft, posted, posted_url, posted_at)
SELECT p.id, 'R_1', 'gridview-maint', 'MEMBER', 'review',
       'Could the handle use the existing useKeyboardResize hook rather than a new listener? We are trying to keep one code path for both.',
       p.url || '#pullrequestreview-1', 'design', now() - INTERVAL '20 hours',
       'Yes -- that is cleaner. I have moved the handle onto useKeyboardResize and dropped the new listener; the arrow-key step stays at 8px to match the mouse snap.',
       FALSE, NULL, NULL
  FROM pull_requests p WHERE p.number = 1601;
INSERT INTO feedback_items (pr_id, external_id, author, author_association, kind, body,
                            classification, created_at, draft, posted, posted_url, posted_at)
SELECT p.id, 'C_1', 'fastcsv-owner', 'OWNER', 'comment',
       'Nice catch. Can you add a test for a quoted header that ends in CRLF too?',
       'needs_reply', now() - INTERVAL '6 days', 'Added test_quoted_header_crlf in the last push.',
       TRUE, p.url || '#issuecomment-2', now() - INTERVAL '5 days'
  FROM pull_requests p WHERE p.number = 412;
UPDATE pull_requests SET unanswered_items = 1 WHERE number = 1601;

INSERT INTO audit (action, slug, detail, actor, at) VALUES
    ('approve', 'example-org__gridview__1544', 'https://github.com/example-org/gridview/issues/1544', 'vimalyad', now() - INTERVAL '4 hours'),
    ('reject', 'demo-project__docforge__950', 'docs typo, not worth a maintainer''s review', 'vimalyad', now() - INTERVAL '10 days'),
    ('pr_open', 'sample-labs__queuectl__301', 'https://github.com/sample-labs/queuectl/pull/318', 'engine', now() - INTERVAL '30 hours');

COMMIT;
