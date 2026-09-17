"""Daily orchestration.

The ordering is the cost control: cheap bars reject candidates before any
expensive work happens. Contest classification and repo facts are a few API
calls; harvest is a large GraphQL query and brief extraction is an LLM call, so
neither runs on a candidate already known to be unusable.
"""

from __future__ import annotations

from . import (audit, brief, contest, discover, harvest, lock, policy,
               propose, repofacts, score, store)
from .models import TransitionError, Candidate, Contest, Status, TARGETABLE, transition


def _reject(cand: Candidate, why: str) -> None:
    cand.reject_reason = why
    if cand.status is Status.DISCOVERED:
        transition(cand, Status.REJECTED, note=why)
    store.save(cand)


def discover_and_propose(
    *, repos: list[str] | None = None, per_repo: int = 8, max_harvest: int = 12,
    dry_run: bool = True,
) -> tuple[list[Candidate], str]:
    print("phase A: discover")
    found = discover.discover(repos=repos, per_repo=per_repo)
    processed: list[Candidate] = []

    print(f"\nphase B: contest classification ({len(found)} candidates)")
    survivors: list[Candidate] = []
    for cand in found:
        verdict, sig = contest.classify(cand)
        cand.contest, cand.pr_signal = verdict, sig
        if verdict not in TARGETABLE:
            note = f"contest={verdict}"
            if sig and sig.reasons:
                note += f" ({sig.reasons[0]})"
            _reject(cand, note)
            processed.append(cand)
            continue
        survivors.append(cand)
    print(f"  {len(survivors)} targetable, {len(processed)} contested/claimed")

    print(f"\nphase B2: cheap bars (repo facts, exclusions)")
    touched = score.repos_touched_by_other_accounts()
    cheap: list[Candidate] = []
    for cand in survivors:
        cand.facts = repofacts.facts(cand.repo)
        ok, fails = score.score(cand, touched=touched)
        # Bars that do not depend on the thread can reject before harvesting.
        blocking = [f for f in fails if "brief" not in f and "maintainer" not in f
                    and "converged" not in f and "claimed by" not in f]
        if blocking:
            cand.score_failures = blocking   # drop bars needing a brief
            _reject(cand, "; ".join(blocking[:2]))
            processed.append(cand)
            continue
        cheap.append(cand)
    print(f"  {len(cheap)} survived cheap bars")

    print(f"\nphase C: harvest + brief (capped at {max_harvest})")
    for cand in cheap[:max_harvest]:
        try:
            payload = harvest.harvest(cand)
            cand.brief = brief.extract(harvest.render_thread(payload))
        except Exception as exc:
            _reject(cand, f"harvest/brief failed: {str(exc)[:120]}")
            processed.append(cand)
            continue

        ok, fails = score.score(cand, touched=touched)
        if ok:
            transition(cand, Status.SCORED, note="cleared all bars")
            transition(cand, Status.PROPOSED, note="awaiting human approval")
            print(f"  PROPOSE  {cand.repo}#{cand.issue}  {cand.title[:50]}")
        else:
            _reject(cand, "; ".join(fails[:2]))
            print(f"  reject   {cand.repo}#{cand.issue}  {fails[0][:60]}")
        store.save(cand)
        processed.append(cand)

    for cand in cheap[max_harvest:]:
        _reject(cand, f"deferred: exceeded max_harvest={max_harvest} this run")
        processed.append(cand)

    path = propose.write(processed)
    audit.record(
        "propose", detail=f"{sum(1 for c in processed if c.status is Status.PROPOSED)} proposed",
        report=str(path), dry_run=dry_run,
    )
    return processed, str(path)


def implement_approved(*, execute: bool = False, limit: int = 1) -> list[str]:
    if execute:
        try:
            with lock.exclusive("implement"):
                return _implement_approved(execute=execute, limit=limit)
        except lock.Busy as exc:
            return [f"skipped: {exc}"]
    return _implement_approved(execute=execute, limit=limit)


def _implement_approved(*, execute: bool = False, limit: int = 1) -> list[str]:
    """Build patches for human-approved candidates, honouring the caps.

    Only APPROVED candidates are eligible -- the state machine makes any other
    path unreachable, so this cannot pick its own targets.
    """
    from . import implement, policy, repo, submit
    from .models import Status

    everything = store.all_candidates()
    queue = [c for c in everything if c.status is Status.APPROVED][:limit]
    if not queue:
        return ["nothing approved"]

    out: list[str] = []
    for cand in queue:
        if cand.blockers:
            out.append(f"{cand.slug}: BLOCKED -- {cand.blockers[0]}")
            continue
        blocked = policy.check_caps(everything, cand)
        if blocked:
            out.append(f"{cand.slug}: held ({blocked[0]})")
            continue

        print(f"\n{cand.repo}#{cand.issue}: {cand.title[:60]}")
        try:
            clone = repo.prepare(cand, execute=execute)
        except Exception as exc:
            # Leave the candidate APPROVED so the next cycle retries it. An
            # unattended run must degrade to "did less", never to "died".
            out.append(f"{cand.slug}: deferred -- {str(exc)[:110]}")
            continue
        if not clone.exists():
            out.append(f"{cand.slug}: [dry-run] no clone")
            continue

        sane, why = repo.clone_is_sane(clone)
        if not sane:
            out.append(f"{cand.slug}: deferred -- {why}")
            continue          # stays APPROVED; next cycle re-clones

        transition(cand, Status.IMPLEMENTING, note="patch in progress")
        store.save(cand)

        branch = (repo.takeover_branch if str(cand.contest) == "stale_pr"
                  else repo.start_branch)(cand, clone)
        print(f"  branch {branch}")

        try:
            result = implement.implement(cand, clone)
        except Exception as exc:
            transition(cand, Status.ABANDONED, note=f"implement raised: {str(exc)[:200]}")
            store.save(cand)
            out.append(f"{cand.slug}: ABANDONED -- {str(exc)[:110]}")
            continue
        if result.get("environment_failure"):
            # Not the patch's fault. Keep the work, block the repo, tell the user.
            cand.blockers = [
                f"{cand.repo} cannot be built or tested on this machine "
                f"(its test environment needs per-repo setup); the patch is "
                f"written but unverified"
            ]
            transition(cand, Status.ABANDONED, note="environment cannot be built locally")
            store.save(cand)
            out.append(f"{cand.slug}: BLOCKED -- cannot verify locally, patch left in {clone}")
            continue

        if not result["ok"]:
            why = (result.get("refusal") or
                   (result.get("verify") or {}).get("violates") or
                   [t for t in result["test_results"] if t["rc"] != 0])
            transition(cand, Status.ABANDONED, note=f"implementation failed: {str(why)[:200]}")
            store.save(cand)
            out.append(f"{cand.slug}: ABANDONED -- {str(why)[:120]}")
            continue

        problems = submit.preflight(cand, clone)
        if problems:
            transition(cand, Status.ABANDONED, note=f"preflight: {problems[0]}")
            store.save(cand)
            out.append(f"{cand.slug}: blocked at preflight -- {problems[0]}")
            continue

        transition(cand, Status.IMPLEMENTED, note=f"tests pass ({result['toolchain']})")
        submit.commit(cand, clone, execute=execute)
        if execute:
            transition(cand, Status.PUSHED, note="pushed to fork")
        url = submit.open_pr(cand, clone, result["summary"], execute=execute)
        if execute and url:
            transition(cand, Status.PR_OPEN, note=url)
        store.save(cand)
        out.append(f"{cand.slug}: {url or '[dry-run] not submitted'}")
    return out


def watch_open(*, execute: bool = False) -> list[str]:
    if execute:
        try:
            with lock.exclusive("watch"):
                return _watch_open(execute=execute)
        except lock.Busy as exc:
            return [f"skipped: {exc}"]
    return _watch_open(execute=execute)


def _watch_open(*, execute: bool = False) -> list[str]:
    from . import policy, replies, report, watch
    live = store.by_status(*policy.OPEN_STATUSES)
    out = []
    for cand in live:
        try:
            out.append(f"{cand.slug}: {watch.sync(cand, execute=execute)}")
        except TransitionError as exc:
            # Categorically different from a network blip: this is our own
            # ordering being wrong, and a silent one loses a merge. Say so.
            audit.record("transition_error", slug=cand.slug, detail=str(exc)[:300])
            out.append(f"{cand.slug}: STATE MACHINE BUG -- {exc}")
        except Exception as exc:
            out.append(f"{cand.slug}: watch failed -- {str(exc)[:120]}")

    # Draft replies for anything newly queued, so a maintainer waiting on an
    # answer shows up as a ready-to-send draft rather than a to-do.
    if execute:
        for cand in store.by_status(*policy.OPEN_STATUSES):
            try:
                if replies.pending(cand):
                    replies.refresh_drafts(cand)
            except Exception:
                pass

    try:
        out.append(f"status -> {report.write()}")
    except Exception as exc:
        out.append(f"status report failed: {str(exc)[:100]}")

    # Mirror to the gist so it is readable away from this machine. A missing
    # scope is a one-line instruction, not a failed run.
    from . import publish
    try:
        out.append(publish.publish())
    except publish.ScopeMissing:
        out.append("gist skipped: PAT lacks `gist` scope "
                   "(add it at github.com/settings/tokens, no new token needed)")
    except publish.Unreachable as exc:
        out.append(f"gist skipped: cannot reach GitHub ({str(exc)[:70]})")
    except Exception as exc:
        out.append(f"gist failed: {str(exc)[:100]}")
    return out or ["no open PRs"]


def _stage(name: str, fn) -> None:
    """Run one daily stage; a failure must not take the others down with it."""
    print(f"\n=== {name} ===")
    try:
        result = fn()
        for line in (result or []):
            print(f"  {line}")
    except Exception as exc:
        print(f"  STAGE FAILED: {type(exc).__name__}: {str(exc)[:200]}")


def daily(*, execute: bool = False) -> None:
    """One full cycle, in dependency order.

    Open PRs are serviced first: an unanswered review on existing work costs
    more than a missed new candidate.
    """
    from . import ledger

    _stage("1. watch open PRs", lambda: watch_open(execute=execute))
    _stage("2. implement approved", lambda: implement_approved(execute=execute))
    _stage("3. discover + propose",
           lambda: [f"report: {discover_and_propose(dry_run=not execute)[1]}"])
    _stage("4. ledger", lambda: [ledger.write()])
