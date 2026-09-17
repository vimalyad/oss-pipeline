# oss-pipeline

Semi-automated open-source contribution pipeline. Finds issues in well-known
repos, proposes them for manual approval, implements the fix, opens a PR as
**`vimalyad`**, then tracks CI / bot / maintainer feedback.

Plan: `~/.claude/plans/i-want-to-contribute-hazy-turing.md`

## Daily use

```bash
pipeline verify                  # identity boundary + token (run after any token rotation)
pipeline discover                # sweep watchlist -> reports/YYYY-MM-DD.md
pipeline approve <slug>          # the human gate
pipeline reject  <slug> --reason "..."
pipeline implement --execute     # build + push + open PR for approved candidates
pipeline watch --execute         # service open PRs
pipeline status                  # what is being tracked
pipeline ledger                  # contribution record -> reports/ledger.md
pipeline daily                   # one full cycle (what launchd runs)
```

**Dry run is the default.** Nothing forks, pushes, commits or opens a PR without
`--execute`.

## Tuning the scorer

```bash
pipeline rescore                 # re-evaluate stored candidates against current
pipeline rescore --apply         # policy.yaml -- no API or LLM calls
```

Edit `config/policy.yaml`, run `rescore`, see what would change. This is the
loop for adjusting bars without re-fetching anything.

## Excluding a repo

```bash
pipeline exclude owner/repo      # before you work on it yourself from the work account
```

Repos already touched by the user's other GitHub accounts are excluded
(those logins live in the gitignored `config/identity.env`, never in a tracked file)
automatically — all three accounts share a display name, so contributions from
two of them are linkable and can read as sockpuppeting.

## Safety

| Rail | Where |
|---|---|
| 4-layer identity isolation | `identity.py`, `githooks/`, `bin/gh-token-helper` |
| Human gate unreachable to skip | `models.TRANSITIONS` |
| Caps: 2 PRs/day, 5 open, 1/repo | `config/policy.yaml`, `policy.check_caps` |
| Never contest an active PR | `contest.py` |
| Secret + workflow scan pre-push | `submit.preflight` |
| Kill switch | `touch state/HALT` |
| Audit log | `state/audit.log` |

## Scheduling

```bash
launchctl load ~/Library/LaunchAgents/com.vimalyad.oss-pipeline.plist
```

Runs `pipeline daily` (no `--execute`) at 09:30. launchd runs a missed job on
wake; cron would skip it.

## Known GitHub behaviour

A **first** PR to any repo has its CI held at `action_required` until a maintainer
approves the run, and that repeats on every push until one of your PRs there
merges. `pipeline watch` reports this as "CI awaiting maintainer approval" and
does not treat it as a failure — there is nothing to fix on our side.

## Tests

```bash
.venv/bin/python -m pytest tests/ -q     # 41 unit tests
tests/test_identity_guards.sh            # 21 guard assertions
```
