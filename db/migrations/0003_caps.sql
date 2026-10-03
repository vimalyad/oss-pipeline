-- Caps, and the transactional claim that makes them hold under parallelism.
--
-- This is the reason the project moved off files. Two concurrent runs over a
-- directory both read "one of five slots used" and both open a pull request;
-- there is no way to fix that in the application. Here the check and the
-- claim are one statement under one lock.

BEGIN;

-- Live configuration, in the database so the dashboard can show what the
-- engine is actually obeying rather than what a YAML file on another machine
-- says. One row.
CREATE TABLE caps (
    id                      SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),

    prs_per_day             INTEGER NOT NULL,
    prs_per_week            INTEGER NOT NULL,
    max_open_prs            INTEGER NOT NULL,
    max_open_per_repo       INTEGER NOT NULL,
    org_cooldown_days       INTEGER NOT NULL,

    max_candidates_per_run  INTEGER NOT NULL,
    max_harvest_per_run     INTEGER NOT NULL,
    reconsider_after_days   INTEGER NOT NULL,

    -- The autonomy path gets a strict subset of the overall budget, so a bug
    -- in the gate cannot consume the whole allowance.
    auto_prs_per_day        INTEGER NOT NULL DEFAULT 0,
    max_open_auto_prs       INTEGER NOT NULL DEFAULT 0,

    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by              TEXT
);

-- Tuned for ten pull requests a week, which is 1.43/day. The per-day figure
-- carries slack because proposals do not arrive evenly: the predecessor put
-- all of them on six days out of twenty-eight.
--
-- max_open_per_repo is 3 rather than 1 and org_cooldown_days is 0, at the
-- user's explicit direction after the campaign risk was put to them. The
-- numbers are here rather than in code precisely so that decision can be
-- reversed without a deployment.
INSERT INTO caps (
    prs_per_day, prs_per_week, max_open_prs, max_open_per_repo, org_cooldown_days,
    max_candidates_per_run, max_harvest_per_run, reconsider_after_days
) VALUES (4, 10, 30, 3, 0, 200, 40, 7);

-- Quality bars, separately, because these are the ones that trade merge rate
-- for volume and they should be legible as a group.
CREATE TABLE quality_bars (
    id                          SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),

    -- Relaxed to hit the weekly target.
    require_maintainer_acceptance BOOLEAN NOT NULL,
    require_converged_thread      BOOLEAN NOT NULL,
    require_tests                 BOOLEAN NOT NULL,
    require_contributing          BOOLEAN NOT NULL,

    -- NOT relaxed, and not a preference. The implementer builds the patch
    -- from the brief; with no stated approach and no acceptance criteria
    -- there is no specification, and the agent returns "no brief to
    -- implement against" rather than inventing one. Turning this off
    -- produces candidates that cannot be implemented, which is a
    -- contradiction rather than a trade.
    require_approach_or_criteria  BOOLEAN NOT NULL DEFAULT TRUE,

    -- Also not relaxed: preflight refuses a diff that touches
    -- .github/workflows, so a candidate whose whole deliverable is a workflow
    -- file can never be completed.
    reject_workflow_changes       BOOLEAN NOT NULL DEFAULT TRUE,

    reject_docs_typo_only         BOOLEAN NOT NULL,
    stale_issue_penalty_years     INTEGER NOT NULL,
    first_time_window_days        INTEGER NOT NULL,

    updated_at                    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by                    TEXT
);

INSERT INTO quality_bars (
    require_maintainer_acceptance, require_converged_thread,
    require_tests, require_contributing,
    reject_docs_typo_only, stale_issue_penalty_years, first_time_window_days
) VALUES (FALSE, FALSE, FALSE, FALSE, TRUE, 3, 90);

-- Staleness windows for contest classification.
CREATE TABLE staleness (
    id                      SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    active_pr_days          INTEGER NOT NULL,
    author_silent_days      INTEGER NOT NULL,
    -- A pull request nobody has reviewed is the project's backlog, not
    -- abandoned work, and its author is waiting rather than stalling. Author
    -- silence only counts as staleness past this much longer window.
    unreviewed_silent_days  INTEGER NOT NULL,
    changes_requested_days  INTEGER NOT NULL,
    ci_red_untouched_days   INTEGER NOT NULL,
    claim_honoured_days     INTEGER NOT NULL,
    pr_untouched_days       INTEGER NOT NULL
);

INSERT INTO staleness VALUES (1, 30, 60, 365, 30, 30, 30, 45);

-- ---------------------------------------------------------------------------
-- The claim
-- ---------------------------------------------------------------------------

-- Returns an empty array when the slot is granted, or the reasons it is not.
--
-- Call it inside the same transaction that writes the pushed/pr_open history
-- row. The advisory lock serialises every claimant, so the count a caller
-- reads cannot change underneath it; without that this is the same race the
-- file-based predecessor had, just with better syntax.
CREATE OR REPLACE FUNCTION claim_pr_slot(p_candidate_id BIGINT)
RETURNS TEXT[] AS $$
DECLARE
    c            caps;
    cand         candidates;
    r            repos;
    reasons      TEXT[] := '{}';
    n_open       INTEGER;
    n_same_repo  INTEGER;
    n_today      INTEGER;
    n_week       INTEGER;
    cooldown_until TIMESTAMPTZ;
BEGIN
    SELECT * INTO c FROM caps WHERE id = 1;
    SELECT * INTO cand FROM candidates WHERE id = p_candidate_id;
    IF cand IS NULL THEN
        RETURN ARRAY['no such candidate'];
    END IF;
    SELECT * INTO r FROM repos WHERE id = cand.repo_id;

    -- One claimant at a time, for the whole cap calculation.
    PERFORM pg_advisory_xact_lock(hashtext('ossp:pr_slot'));

    SELECT count(*) INTO n_open FROM candidates
     WHERE status IN ('pushed','pr_open','changes_requested','updating','stale');
    IF n_open >= c.max_open_prs THEN
        reasons := reasons || format('%s pull requests already open (cap %s)', n_open, c.max_open_prs);
    END IF;

    SELECT count(*) INTO n_same_repo FROM candidates
     WHERE repo_id = cand.repo_id
       AND status IN ('pushed','pr_open','changes_requested','updating','stale');
    IF n_same_repo >= c.max_open_per_repo THEN
        reasons := reasons || format('%s already has %s open pull request(s) (cap %s)',
                                     r.full_name, n_same_repo, c.max_open_per_repo);
    END IF;

    -- Counted from history, not from current status: a pull request opened
    -- this morning and merged this afternoon still spent today's budget.
    SELECT count(DISTINCT h.candidate_id) INTO n_today
      FROM candidate_history h
     WHERE h.to_status = 'pr_open' AND h.at >= date_trunc('day', now());
    IF n_today >= c.prs_per_day THEN
        reasons := reasons || format('%s pull requests opened today (cap %s)', n_today, c.prs_per_day);
    END IF;

    SELECT count(DISTINCT h.candidate_id) INTO n_week
      FROM candidate_history h
     WHERE h.to_status = 'pr_open' AND h.at >= now() - INTERVAL '7 days';
    IF n_week >= c.prs_per_week THEN
        reasons := reasons || format('%s pull requests opened in the last 7 days (cap %s)',
                                     n_week, c.prs_per_week);
    END IF;

    IF c.org_cooldown_days > 0 THEN
        SELECT max(h.at) + (c.org_cooldown_days || ' days')::INTERVAL
          INTO cooldown_until
          FROM candidate_history h
          JOIN candidates c2 ON c2.id = h.candidate_id
          JOIN repos r2 ON r2.id = c2.repo_id
         WHERE h.to_status = 'pr_open' AND r2.owner = r.owner AND c2.id <> cand.id;
        IF cooldown_until IS NOT NULL AND cooldown_until > now() THEN
            reasons := reasons || format('%s is in cooldown until %s',
                                         r.owner, to_char(cooldown_until, 'DD Mon'));
        END IF;
    END IF;

    RETURN reasons;
END;
$$ LANGUAGE plpgsql;

COMMIT;
