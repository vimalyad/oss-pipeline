-- Pull requests, their observed state over time, and the work queue the
-- parallel workers draw from.

BEGIN;

CREATE TABLE pull_requests (
    id              BIGSERIAL PRIMARY KEY,
    candidate_id    BIGINT NOT NULL REFERENCES candidates(id) ON DELETE CASCADE,
    repo_id         BIGINT NOT NULL REFERENCES repos(id),
    number          INTEGER NOT NULL,
    url             TEXT NOT NULL,
    title           TEXT NOT NULL,
    head_branch     TEXT NOT NULL,
    head_sha        TEXT,
    base_branch     TEXT NOT NULL,
    state           pr_state NOT NULL DEFAULT 'open',

    -- What the dashboard shows without a second query.
    checks_total    INTEGER NOT NULL DEFAULT 0,
    checks_failing  INTEGER NOT NULL DEFAULT 0,
    checks_pending  INTEGER NOT NULL DEFAULT 0,
    review_decision TEXT,                     -- GitHub's own, verbatim
    reviewer_count  INTEGER NOT NULL DEFAULT 0,
    unanswered_items INTEGER NOT NULL DEFAULT 0,
    additions       INTEGER,
    deletions        INTEGER,
    changed_files   INTEGER,

    opened_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_polled_at  TIMESTAMPTZ,
    -- GitHub's own updatedAt, not ours. Staleness is measured against the
    -- project's clock, not against how often we happened to poll.
    remote_updated_at TIMESTAMPTZ,
    closed_at       TIMESTAMPTZ,
    merged_at       TIMESTAMPTZ,

    UNIQUE (repo_id, number)
);

CREATE INDEX pull_requests_live ON pull_requests (state)
    WHERE state NOT IN ('merged', 'closed');
CREATE INDEX pull_requests_candidate ON pull_requests (candidate_id);

-- Every state change, so the dashboard can draw a timeline and so "when did
-- this go red" is answerable without re-reading GitHub.
CREATE TABLE pr_state_history (
    id          BIGSERIAL PRIMARY KEY,
    pr_id       BIGINT NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    from_state  pr_state,
    to_state    pr_state NOT NULL,
    detail      TEXT,
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX pr_state_history_pr ON pr_state_history (pr_id, at DESC);

-- Feedback from maintainers, and our answer to it. Nothing here is posted
-- without a person reading the draft first.
CREATE TABLE feedback_items (
    id              BIGSERIAL PRIMARY KEY,
    pr_id           BIGINT NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    external_id     TEXT NOT NULL,            -- GitHub's node id, for dedupe
    author          TEXT NOT NULL,
    author_association TEXT,
    kind            TEXT NOT NULL,            -- review | comment | review_thread
    body            TEXT NOT NULL,
    url             TEXT,
    classification  TEXT,                     -- design | nit | question | ci | needs_reply
    created_at      TIMESTAMPTZ NOT NULL,

    draft           TEXT,
    draft_rejected  TEXT[] NOT NULL DEFAULT '{}',
    posted          BOOLEAN NOT NULL DEFAULT FALSE,
    posted_url      TEXT,
    posted_at       TIMESTAMPTZ,

    UNIQUE (pr_id, external_id)
);

CREATE INDEX feedback_unanswered ON feedback_items (pr_id) WHERE NOT posted;

-- ---------------------------------------------------------------------------
-- The work queue
-- ---------------------------------------------------------------------------

-- Parallel workers draw from here with FOR UPDATE SKIP LOCKED, which is the
-- whole reason this project moved off files: two concurrent runs over a
-- directory both read "a slot is free" and both open a pull request.
CREATE TYPE job_kind AS ENUM ('discover', 'triage', 'implement', 'watch', 'reply', 'report');
CREATE TYPE job_state AS ENUM ('queued', 'running', 'done', 'failed', 'cancelled');

CREATE TABLE jobs (
    id            BIGSERIAL PRIMARY KEY,
    kind          job_kind NOT NULL,
    candidate_id  BIGINT REFERENCES candidates(id) ON DELETE CASCADE,
    repo_id       BIGINT REFERENCES repos(id),
    state         job_state NOT NULL DEFAULT 'queued',
    priority      INTEGER NOT NULL DEFAULT 0,       -- higher runs first

    worker        TEXT,                             -- which container holds it
    attempts      INTEGER NOT NULL DEFAULT 0,
    max_attempts  INTEGER NOT NULL DEFAULT 3,
    last_error    TEXT,

    -- A worker that dies holds its row forever without this. The reaper
    -- requeues anything whose lease has expired, which is what makes the
    -- thing fault tolerant rather than merely concurrent.
    lease_until   TIMESTAMPTZ,

    queued_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ
);

CREATE INDEX jobs_ready ON jobs (kind, priority DESC, queued_at) WHERE state = 'queued';
CREATE INDEX jobs_expired ON jobs (lease_until) WHERE state = 'running';

-- One implement job per candidate at a time. Two workers patching the same
-- clone would corrupt it, and the clone is on a shared volume.
CREATE UNIQUE INDEX jobs_one_live_per_candidate
    ON jobs (candidate_id, kind)
    WHERE state IN ('queued', 'running') AND candidate_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Audit: append only
-- ---------------------------------------------------------------------------

-- This is the table that recovered sixteen destroyed human decisions in the
-- predecessor, so it is the one table the application may not edit. Writes
-- are inserts; updates and deletes are refused by the database.
CREATE TABLE audit (
    id            BIGSERIAL PRIMARY KEY,
    action        TEXT NOT NULL,
    slug          TEXT,
    detail        TEXT,
    actor         TEXT,
    at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX audit_action ON audit (action, at DESC);
CREATE INDEX audit_slug ON audit (slug, at DESC);

CREATE OR REPLACE FUNCTION audit_is_append_only() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'the audit log is append only (attempted %)', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit
    FOR EACH STATEMENT EXECUTE FUNCTION audit_is_append_only();
CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit
    FOR EACH STATEMENT EXECUTE FUNCTION audit_is_append_only();

-- Notification dedupe. The watcher runs hourly; without this one failing
-- check notifies roughly a hundred times.
CREATE TABLE notifications_sent (
    dedupe_key  TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    sent_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMIT;
