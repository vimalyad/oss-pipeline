-- The candidate fields the engine needs back on its next run.
--
-- 0001 modelled what the dashboard shows. The engine also keeps working state
-- on a candidate -- which pull request it opened, which feedback it has
-- already read, the replies waiting on a person, the repository facts the
-- score was computed against -- and a store that drops any of those does not
-- fail loudly. It re-reads every review as new, forgets the pull request it
-- is watching, and scores against facts it no longer has. So they are columns
-- here, and the round-trip test asserts each one.

BEGIN;

ALTER TABLE candidates
    -- The engine's own record of its pull request. pull_requests is the
    -- dashboard's richer view of the same thing, written by the watcher; this
    -- is the pointer the watcher needs in order to find it at all.
    ADD COLUMN pr_number       INTEGER,
    ADD COLUMN pr_url          TEXT,
    -- Feedback ids already read. Without it every cycle reports every review
    -- again, and the phone notifications follow.
    ADD COLUMN watch_seen      TEXT[] NOT NULL DEFAULT '{}',
    -- Items waiting on a person, with their drafts. JSONB because the shape is
    -- the engine's hand-editable map, and feedback_items is derived from it.
    ADD COLUMN queued_replies  JSONB NOT NULL DEFAULT '[]',
    ADD COLUMN disclosed_ai    BOOLEAN NOT NULL DEFAULT FALSE,
    -- The repository facts as they were when this candidate was scored. Not
    -- a join to repos: those are refreshed weekly, and a rejection has to be
    -- explainable by the facts it was actually made on.
    ADD COLUMN facts           JSONB;

-- repos keeps the gates the dashboard filters on as columns; the full record,
-- including the eligibility rules no column was made for, rides along here so
-- a cached read returns exactly what was fetched.
ALTER TABLE repos ADD COLUMN facts JSONB;

COMMIT;
