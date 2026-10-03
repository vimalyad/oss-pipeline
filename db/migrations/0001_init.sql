-- The shared contract between the Go engine and the Spring Boot API.
--
-- Two services write here, so the invariants live in the database rather than
-- in either one of them. The predecessor kept state in JSON files and lost
-- sixteen human decisions to a schema difference between two readers of the
-- same directory; the lesson taken is not "use a database" but "put the rule
-- where both readers must obey it".

BEGIN;

-- ---------------------------------------------------------------------------
-- Enumerations
-- ---------------------------------------------------------------------------

-- The candidate lifecycle. Written out rather than derived, and the allowed
-- edges are a table below rather than application logic, because the thing
-- that went wrong before was an edge nobody had written down.
CREATE TYPE candidate_status AS ENUM (
    'discovered',
    'scored',
    'proposed',
    'approved',        -- written only by a human
    'auto_approved',   -- written only by the autonomy gate, never by a person
    'rejected',
    'implementing',
    'implemented',
    'abandoned',
    'pushed',
    'pr_open',
    'changes_requested',
    'updating',
    'merged',
    'closed',
    'stale'
);

-- What a pull request looks like to someone watching it. Richer than the
-- candidate status because the dashboard needs to distinguish "nobody has
-- looked" from "a human asked for changes" from "CI is red", which the
-- lifecycle collapses.
CREATE TYPE pr_state AS ENUM (
    'draft',
    'open',               -- exists, no review yet
    'review_required',    -- the project requires a review before merge
    'under_review',       -- a review has started (comments, no verdict)
    'changes_requested',
    'approved',           -- approved, not yet merged
    'ci_failing',
    'ci_pending',
    'conflicted',         -- needs a rebase
    'updating',           -- we are pushing a fix right now
    'stale',              -- nothing has happened for a long time
    'blocked_needs_human',-- the pipeline stopped and wants a person
    'merged',
    'closed'
);

-- Why a candidate was turned down. The distinction is load-bearing: a
-- transient reason comes back for another look, a permanent one never does,
-- and a human decision is never revisited by a machine.
CREATE TYPE rejection_kind AS ENUM (
    'human',        -- a person decided; never reconsidered
    'structural',   -- will never be true (platform we cannot verify, a CLA we will not sign)
    'transient',    -- true today only (claimed, contested, deferred by a cap)
    'quality'       -- failed a configurable bar; comes back if the bar moves
);

CREATE TYPE contest_class AS ENUM ('no_pr', 'stale_pr', 'active_pr', 'claimed', 'unknown');

-- ---------------------------------------------------------------------------
-- Repositories
-- ---------------------------------------------------------------------------

CREATE TABLE repos (
    id                  BIGSERIAL PRIMARY KEY,
    full_name           TEXT NOT NULL UNIQUE,          -- owner/name
    owner               TEXT NOT NULL,
    primary_language    TEXT,
    stars               INTEGER,
    topics              TEXT[] NOT NULL DEFAULT '{}',

    -- Gates that belong to the project rather than to any one issue.
    has_tests           BOOLEAN,
    has_contributing    BOOLEAN,
    requires_dco        BOOLEAN,
    requires_cla        BOOLEAN,
    cla_signed          BOOLEAN NOT NULL DEFAULT FALSE,
    bans_ai_prs         BOOLEAN,
    requires_ai_disclosure BOOLEAN,
    ai_policy_quote     TEXT,

    -- Discovery control.
    on_watchlist        BOOLEAN NOT NULL DEFAULT TRUE,
    tier                SMALLINT NOT NULL DEFAULT 1,
    domain              TEXT,
    excluded            BOOLEAN NOT NULL DEFAULT FALSE,
    excluded_reason     TEXT,

    -- A repository another of the user's accounts has contributed to. Two
    -- pull requests on one project from two accounts sharing a display name
    -- reads as sockpuppeting however innocent the intent.
    touched_by_other_account BOOLEAN NOT NULL DEFAULT FALSE,

    facts_fetched_at    TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX repos_watchlist ON repos (on_watchlist, excluded) WHERE on_watchlist AND NOT excluded;
CREATE INDEX repos_owner ON repos (owner);

-- ---------------------------------------------------------------------------
-- Candidates
-- ---------------------------------------------------------------------------

CREATE TABLE candidates (
    id                  BIGSERIAL PRIMARY KEY,
    repo_id             BIGINT NOT NULL REFERENCES repos(id),
    issue_number        INTEGER NOT NULL,
    slug                TEXT NOT NULL UNIQUE,          -- owner__name__issue, kept for continuity
    title               TEXT NOT NULL,
    url                 TEXT NOT NULL,
    status              candidate_status NOT NULL DEFAULT 'discovered',

    labels              TEXT[] NOT NULL DEFAULT '{}',
    comment_count       INTEGER NOT NULL DEFAULT 0,
    reaction_count      INTEGER NOT NULL DEFAULT 0,
    issue_created_at    TIMESTAMPTZ,
    issue_updated_at    TIMESTAMPTZ,

    contest             contest_class NOT NULL DEFAULT 'unknown',
    -- The open pull requests the issue's timeline points at, as discovery saw
    -- them. Kept rather than consumed in the same run so triage is a separate
    -- stage that need not ask GitHub again.
    linked_prs          INTEGER[] NOT NULL DEFAULT '{}',

    reject_kind         rejection_kind,
    reject_reason       TEXT,
    rejected_at         TIMESTAMPTZ,

    -- Soft penalties rank a candidate below cleaner ones; blockers need a
    -- one-off human action first. Neither is a rejection.
    soft_penalties      TEXT[] NOT NULL DEFAULT '{}',
    blockers            TEXT[] NOT NULL DEFAULT '{}',
    score_failures      TEXT[] NOT NULL DEFAULT '{}',

    branch              TEXT,
    -- Whether the branch actually continued someone else's commits. The pull
    -- request body says one of two different things depending on it and only
    -- one of them is true.
    took_over           BOOLEAN NOT NULL DEFAULT FALSE,
    credits             TEXT,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (repo_id, issue_number),
    -- A rejection must say which kind it is, or the reconsider logic cannot
    -- tell a decision from a deferral.
    CONSTRAINT rejection_is_explained CHECK (
        (status <> 'rejected') OR (reject_kind IS NOT NULL AND reject_reason IS NOT NULL)
    )
);

CREATE INDEX candidates_status ON candidates (status);
CREATE INDEX candidates_repo ON candidates (repo_id);
CREATE INDEX candidates_reconsider ON candidates (reject_kind, rejected_at)
    WHERE status = 'rejected';

-- The extracted specification. Separate from candidates because it is large
-- and because a candidate without one is a legitimate state.
CREATE TABLE briefs (
    candidate_id        BIGINT PRIMARY KEY REFERENCES candidates(id) ON DELETE CASCADE,
    maintainer_approach TEXT,
    approach_source_url TEXT,
    -- OWNER, MEMBER or COLLABORATOR. Anything else is not a maintainer, and
    -- an approach attributed to anything else is dropped rather than trusted.
    approach_association TEXT,
    rejected_approaches TEXT[] NOT NULL DEFAULT '{}',
    acceptance_criteria TEXT[] NOT NULL DEFAULT '{}',
    open_questions      TEXT[] NOT NULL DEFAULT '{}',
    claimed_by          TEXT,
    claimed_at          TIMESTAMPTZ,
    reproduction        TEXT,
    -- Every field the post-checks removed, with the reason. A brief that is
    -- empty because the model was careful and one that is empty because it
    -- invented a quote look identical from the outside.
    dropped             TEXT[] NOT NULL DEFAULT '{}',
    extracted_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Contest evidence for the pull request that most justifies a verdict.
CREATE TABLE pr_signals (
    candidate_id            BIGINT PRIMARY KEY REFERENCES candidates(id) ON DELETE CASCADE,
    pr_number               INTEGER NOT NULL,
    url                     TEXT,
    author                  TEXT,
    is_draft                BOOLEAN NOT NULL DEFAULT FALSE,
    days_since_commit       INTEGER,
    days_since_author_comment INTEGER,
    days_since_changes_requested INTEGER,
    -- Whether anyone from the project has reviewed it at all. This is what
    -- separates "the author gave up" from "the maintainers have not got to
    -- it", which look identical from author silence alone.
    reviewed                BOOLEAN NOT NULL DEFAULT FALSE,
    has_stale_label         BOOLEAN NOT NULL DEFAULT FALSE,
    checks_failing          BOOLEAN NOT NULL DEFAULT FALSE,
    reasons                 TEXT[] NOT NULL DEFAULT '{}',
    observed_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- The state machine, as data
-- ---------------------------------------------------------------------------

CREATE TABLE allowed_transitions (
    from_status candidate_status NOT NULL,
    to_status   candidate_status NOT NULL,
    PRIMARY KEY (from_status, to_status)
);

INSERT INTO allowed_transitions (from_status, to_status) VALUES
    ('discovered','scored'), ('discovered','rejected'),
    ('scored','proposed'), ('scored','rejected'),
    -- The human gate is structural: there is deliberately no edge from
    -- discovered, scored or proposed to implementing, so no unattended run
    -- reaches implementation by drifting through this table.
    ('proposed','approved'), ('proposed','auto_approved'), ('proposed','rejected'),
    ('approved','implementing'), ('approved','abandoned'), ('approved','rejected'),
    ('auto_approved','implementing'), ('auto_approved','abandoned'), ('auto_approved','rejected'),
    ('implementing','implemented'), ('implementing','abandoned'),
    ('implemented','pushed'), ('implemented','abandoned'),
    -- GitHub, not this pipeline, decides merge and close, so every status the
    -- watcher can observe must accept both. The predecessor omitted
    -- changes_requested -> merged while a real pull request sat in exactly
    -- that state, and the resulting error was swallowed.
    ('pushed','pr_open'), ('pushed','abandoned'), ('pushed','merged'), ('pushed','closed'),
    ('pr_open','changes_requested'), ('pr_open','updating'), ('pr_open','merged'),
    ('pr_open','closed'), ('pr_open','stale'),
    ('changes_requested','updating'), ('changes_requested','pr_open'),
    ('changes_requested','merged'), ('changes_requested','closed'), ('changes_requested','stale'),
    ('updating','pr_open'), ('updating','abandoned'), ('updating','merged'),
    ('updating','closed'), ('updating','stale'),
    ('stale','pr_open'), ('stale','updating'), ('stale','merged'), ('stale','closed');

-- The only sanctioned bypasses. Each exists because a human deliberately
-- revisits a terminal decision, and each is recorded as forced with an actor.
CREATE TABLE reopen_edges (
    from_status candidate_status NOT NULL,
    to_status   candidate_status NOT NULL,
    command     TEXT NOT NULL,
    PRIMARY KEY (from_status, to_status)
);

INSERT INTO reopen_edges VALUES
    ('rejected','proposed','rescore --apply'),
    ('abandoned','approved','retry');

CREATE TABLE candidate_history (
    id            BIGSERIAL PRIMARY KEY,
    candidate_id  BIGINT NOT NULL REFERENCES candidates(id) ON DELETE CASCADE,
    from_status   candidate_status,
    to_status     candidate_status NOT NULL,
    note          TEXT,
    -- A sanctioned bypass of allowed_transitions, with who did it. The
    -- predecessor had these hand-written into JSON with invented timestamps
    -- and no way to tell them from edges the machine had allowed.
    forced        BOOLEAN NOT NULL DEFAULT FALSE,
    actor         TEXT,
    at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX candidate_history_candidate ON candidate_history (candidate_id, at);
CREATE INDEX candidate_history_to ON candidate_history (to_status, at);

-- Enforce the machine in the database, so neither service can write an edge
-- the other does not know about.
CREATE OR REPLACE FUNCTION enforce_transition() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.from_status IS NULL THEN
        RETURN NEW;                                  -- the initial insert
    END IF;
    IF NEW.forced THEN
        IF NOT EXISTS (SELECT 1 FROM reopen_edges
                       WHERE from_status = NEW.from_status AND to_status = NEW.to_status) THEN
            RAISE EXCEPTION 'forced edge %->% is not a sanctioned reopen',
                NEW.from_status, NEW.to_status;
        END IF;
        IF NEW.actor IS NULL OR NEW.actor = '' THEN
            RAISE EXCEPTION 'a forced edge must record who forced it';
        END IF;
        RETURN NEW;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM allowed_transitions
                   WHERE from_status = NEW.from_status AND to_status = NEW.to_status) THEN
        RAISE EXCEPTION 'illegal transition %->%', NEW.from_status, NEW.to_status;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER candidate_history_legal
    BEFORE INSERT ON candidate_history
    FOR EACH ROW EXECUTE FUNCTION enforce_transition();

COMMIT;
