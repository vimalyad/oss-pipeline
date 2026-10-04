# oss-pipeline

Finds issues in well-known open-source projects, proposes them for approval,
writes and verifies the patch in a container, opens the pull request as
`vimalyad`, and tracks what happens to it.

Target throughput is **10 pull requests a week**.

## Shape

```
                  ┌──────────────┐
   GitHub ◀────────│  Go engine   │  discover → triage → implement → submit → watch
                  └──────┬───────┘  holds the only GitHub token
                         │ writes
                  ┌──────▼───────┐
                  │   Postgres   │  state machine, caps and audit live HERE,
                  └──────▲───────┘  not in either service
                         │ reads
                  ┌──────┴───────┐
   Browser ◀───────│ Spring Boot  │  REST + TypeScript dashboard + Actuator
                  └──────────────┘  may approve and reject; may NOT publish
```

Two languages on purpose. The engine inherits a test suite that encodes things
somebody had to find out the hard way — Docker forces `noexec` on `--tmpfs`;
`bash -lc` sources `/etc/profile` and erases the image's PATH so `go`
disappears from `golang` images; Go slices strings by byte so UTF-8
truncation splits characters; a `.git/config` credential helper survives
`GIT_CONFIG_KEY` overrides. Rewriting that in another language re-enters the
minefield at step one. The API is where new surface is being added, so it is
where the new language goes.

## Why the rules are in the database

The predecessor kept state in JSON files and lost **sixteen human decisions**
in one scheduled sweep: the newer binary wrote two fields the older reader had
no slot for, hydration raised `TypeError`, and `should_reconsider` read that
as "never seen before", so discovery rebuilt the candidates from a search
result and saved over the top. A second instance of the same bug, one function
away, took a whole day's discovery down before it was found.

The lesson taken is not "use a database". It is **put the rule where every
reader must obey it**. So:

- `allowed_transitions` is a table and a trigger refuses any edge not in it.
  The approval gate is therefore enforced by Postgres: `proposed → implementing`
  fails at the database, not in application code.
- `reopen_edges` are the only sanctioned bypasses, and a forced edge with no
  `actor` is refused.
- `audit` rejects `UPDATE` and `DELETE` outright. It is append-only because it
  is what recovered those sixteen decisions.
- `claim_pr_slot()` takes an advisory lock, so two parallel workers cannot both
  take the last slot. That race cannot be fixed in a file-based store, and it
  is the actual reason for Postgres.

## Caps

Live in the `caps` and `quality_bars` tables, not in code, so the dashboard
shows what the engine is really obeying and a decision can be reversed without
a deployment.

| | |
|---|---|
| 4/day, 10/week | the target |
| 30 open, 3 per repo, no org cooldown | relaxed, deliberately — see below |
| `max_harvest_per_run` 40 | the predecessor deferred 89 candidates unassessed at 12 |

Two bars are **not** configurable-off, and the reason is structural rather
than a preference:

- **`require_approach_or_criteria`** — the implementer builds the patch from
  the brief. With no stated approach and no acceptance criteria there is no
  specification, and the agent returns "no brief to implement against" rather
  than inventing one. Turning it off produces candidates that cannot be
  implemented.
- **`reject_workflow_changes`** — preflight refuses a diff touching
  `.github/workflows`, so a candidate whose whole deliverable is a workflow
  file can never be completed.

`max_open_per_repo: 3` and `org_cooldown_days: 0` are set at the operator's
explicit direction. The risk accepted: several open pull requests on one
project from one outside contributor reads as a campaign rather than as
contributions, and maintainers begin closing on sight. 10/week across 10
projects does not; across 3 it does. Widening the watchlist is the cheaper way
to the same number.

## Pull request states

Richer than the candidate lifecycle, because a dashboard has to tell "nobody
has looked" from "a human asked for changes" from "CI is red", which the
lifecycle collapses into one status:

`draft` · `open` · `review_required` · `under_review` · `changes_requested` ·
`approved` · `ci_failing` · `ci_pending` · `conflicted` · `updating` ·
`stale` · `blocked_needs_human` · `merged` · `closed`

## Running it

```bash
cp .env.example .env && chmod 600 .env     # fill in GH_TOKEN and the identity
docker compose up -d --build
open http://localhost:8080
```

Dry run is the default everywhere. Nothing forks, commits, pushes or opens a
pull request without `--execute`.

The engine uses Postgres whenever `DATABASE_URL` is set, and the JSON files
under `state/` otherwise. A set but unreachable database is an error, never a
quiet fall back to files. To move existing JSON state across (dry run first):

```bash
DATABASE_URL=postgres://... pipeline import-json            # what would be copied
DATABASE_URL=postgres://... pipeline import-json --execute
```

## The dashboard

`api/` (Spring Boot) and `web/` (TypeScript, React) ship as one image: the
frontend is built into the jar and served from the same origin.

| Page | What it answers |
|---|---|
| Overview | the caps and how much of each is spent; what is waiting on you, replies first; weekly throughput; the funnel |
| Proposals | each proposal with its brief and contest evidence, and approve / reject |
| Pull requests | every pull request by state, with checks, review and diff size |
| Pull request | one timeline of pipeline decisions, GitHub state and maintainer feedback, with drafted replies |
| Candidates | everything ever looked at, by stage, with rejection reasons |
| Audit log | the append-only record |

Approve and reject write the status, the history edge and the audit row in one
transaction, and the history trigger is the final word on legality: an edge
the table does not allow is refused by Postgres and reported in the page.
A rejection needs a reason, and a rejection made here is never reconsidered.
The engine refuses to save over a decision made after it loaded the candidate.

The port is bound to `127.0.0.1`. There is no login, so whoever can reach the
port can approve; reach it from elsewhere through an SSH tunnel.

Working on it locally:

```bash
# a database with the real migrations and fictional data
podman run -d --name ossp-dev-pg -e POSTGRES_DB=ossp -e POSTGRES_USER=ossp \
  -e POSTGRES_PASSWORD=dev -p 5432:5432 postgres:17-alpine
for f in db/migrations/*.sql api/dev/seed.sql; do
  podman exec -i ossp-dev-pg psql -q -U ossp -d ossp < "$f"; done

(cd api && SPRING_DATASOURCE_PASSWORD=dev mvn spring-boot:run)   # :8080
(cd web && npm install && npm run dev)                            # :5173, proxies /api
```

`api/dev/seed.sql` is made up and must never be loaded into the real
database. Tests: `go test ./...` in `engine/`, `mvn test` in `api/` and
`npm test` in `web/`. The database-backed ones run when
`OSSP_TEST_DATABASE_URL` (engine) or `OSSP_TEST_JDBC_URL` (api) points at a
database with the migrations applied, and skip otherwise.

## Hard rules, unchanged

- **No AI attribution anywhere in git history.** Enforced twice: a commit-msg
  hook, and an in-process check against the same pattern file, because a clone
  whose `core.hooksPath` was rewritten still has to be caught. Disclosure goes
  in the pull request body, and only where the project asks for it.
- **One identity.** Every clone is hardened and re-asserted before any push,
  including the `fork` remote's URL — three layers guarded what goes *into* a
  commit and nothing guarded where the commit goes.
- **No credential ever enters a sandbox.** The agent edits files in a
  bind-mounted clone; every command runs in a container the pipeline drives.
  The agent also gets `--strict-mcp-config`, because without it the CLI loads
  whatever MCP servers the host has configured and an agent working inside a
  third-party clone quietly acquired browser automation.
- **The autonomous gate approves, not a person.** Since 2026-10-04 nobody
  approves proposals by hand: `policy.yaml` `autonomy.mode: standing` is the
  operator's standing authorisation, and every proposal goes through
  `internal/autogate` in the daily run (`pipeline auto-approve`). Its approvals
  are a separate, labelled status (`auto_approved`) and an `auto_approve` audit
  row, so every pull request is still attributable to the rule that let it
  through. What it checks, all required:
  - HALT, checked first; `pipeline halt` stops the next approval.
  - Autonomous caps, ramped: 2 a day, 5 a week, 10 open.
  - One autonomous pull request per repository until something of ours
    merges there, and none after one is closed unmerged.
  - A bar stricter than the manual one: no soft penalty, no pull request
    from anyone else on the issue, an approach stated by a maintainer, and
    a repository that does not ban automated contributions, does not
    require an AI disclosure (which would assert a review nobody did), and
    has no unsigned CLA.

  A cap or the per-repository limit holds a proposal for the next run;
  anything else rejects it with every reason recorded, since nobody would
  ever come back to it. To approve by hand again, set `autonomy.mode: off`.

## Moving to another machine

The flow runs on one machine at a time. Two machines both acting as
`vimalyad` against separate state stores will open duplicate pull requests on
the same issues, which is worse than either running alone.

Before starting here, stop the predecessor:

```bash
launchctl bootout gui/$(id -u)/com.vimalyad.oss-pipeline
launchctl bootout gui/$(id -u)/com.vimalyad.oss-pipeline-watch
```

The old repository is archived at
[`vimalyad/oss-pipeline-stale`](https://github.com/vimalyad/oss-pipeline-stale);
its JSON state is the migration source for the first import.
