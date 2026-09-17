"""Pipeline CLI. Commands are added as the build order progresses."""

from __future__ import annotations

import argparse
import subprocess
import sys
from pathlib import Path

from . import identity


def _verify(_args: argparse.Namespace) -> int:
    """Check the identity boundary. Run this before trusting anything else."""
    ident = identity.load_identity()
    root = ident.root
    failures = 0

    def line(ok: bool, msg: str) -> None:
        nonlocal failures
        print(f"  {'ok  ' if ok else 'FAIL'}  {msg}")
        if not ok:
            failures += 1

    print("config")
    line(ident.login == "vimalyad", f"OSS login is {ident.login}")
    line(ident.helper_path.exists(), f"credential helper present at {ident.helper_path}")
    line((root / "githooks" / "pre-push").exists(), "pre-push guard present")
    line((root / "githooks" / "commit-msg").exists(), "commit-msg guard present")

    print("guard test suites")
    for suite in (
        ["/bin/sh", str(root / "tests" / "test_identity_guards.sh")],
        [sys.executable, "-m", "pytest", str(root / "tests"), "-q"],
    ):
        proc = subprocess.run(suite, capture_output=True, text=True, cwd=root)
        line(proc.returncode == 0, f"{Path(suite[-2] if '-q' in suite else suite[-1]).name} passed")

    print("keychain + token")
    try:
        identity.token(ident)
        line(True, "PAT readable from Keychain")
    except identity.IdentityError as exc:
        line(False, str(exc).splitlines()[0])
        print(f"\n{failures} check(s) failed. Create the PAT first:")
        print(f"  security add-generic-password -a {ident.keychain_account} "
              f"-s {ident.keychain_service} -w")
        return 1

    try:
        login = identity.assert_token_account(ident)
        line(True, f"token authenticates as {login}")
    except identity.IdentityError as exc:
        line(False, str(exc))

    # The token must NOT be able to see private repos or org data.
    proc = subprocess.run(
        ["gh", "api", "user", "--jq", ".plan.name // \"n/a\""],
        capture_output=True, text=True, env=identity.pipeline_env(ident),
    )
    scopes = subprocess.run(
        ["gh", "api", "-i", "user"], capture_output=True, text=True,
        env=identity.pipeline_env(ident),
    ).stdout
    scope_line = next(
        (l.split(":", 1)[1].strip() for l in scopes.splitlines()
         if l.lower().startswith("x-oauth-scopes:")), ""
    )
    line("repo" not in scope_line.replace("public_repo", ""),
         f"token scopes are minimal: [{scope_line}]")
    line("read:org" not in scope_line, "token has no org access")

    # An unattended daily job must not discover token expiry by silently
    # failing, so surface it here and warn while there is still time to rotate.
    expiry = next(
        (l.split(":", 1)[1].strip() for l in scopes.splitlines()
         if l.lower().startswith("github-authentication-token-expiration:")), ""
    )
    if expiry:
        from datetime import datetime, timezone
        when = datetime.strptime(expiry.replace(" UTC", ""), "%Y-%m-%d %H:%M:%S").replace(
            tzinfo=timezone.utc)
        days = (when - datetime.now(timezone.utc)).days
        line(days > 14, f"token expires in {days} days ({when:%Y-%m-%d})"
                        + ("" if days > 14 else " -- ROTATE NOW"))
    else:
        print("  warn  token has no expiry date set")

    print()
    print("identity boundary VERIFIED" if failures == 0
          else f"{failures} check(s) FAILED -- do not proceed")
    return 1 if failures else 0


def _discover(args: argparse.Namespace) -> int:
    from . import run
    cands, report = run.discover_and_propose(
        repos=args.repo or None, per_repo=args.per_repo,
        max_harvest=args.max_harvest, dry_run=not args.execute,
    )
    print(f"\nreport: {report}")
    return 0


def _approve(args: argparse.Namespace) -> int:
    from . import gate
    print(gate.approve(args.slug))
    return 0


def _reject_cmd(args: argparse.Namespace) -> int:
    from . import gate
    print(gate.reject(args.slug, args.reason))
    return 0


def _exclude(args: argparse.Namespace) -> int:
    from . import gate
    print(gate.exclude(args.repo))
    return 0


def _status(args: argparse.Namespace) -> int:
    from collections import Counter
    from . import store
    cands = store.all_candidates()
    counts = Counter(str(c.status) for c in cands)
    print(f"{len(cands)} candidates tracked")
    for status, n in sorted(counts.items(), key=lambda kv: -kv[1]):
        print(f"  {n:4d}  {status}")
    live = [c for c in cands if str(c.status) in
            ("proposed", "approved", "pushed", "pr_open", "changes_requested", "updating")]
    if live:
        print("\nawaiting action:")
        for c in live:
            print(f"  {str(c.status):20s} {c.repo}#{c.issue}  {c.title[:46]}")
    return 0


def _lines(rows) -> int:
    for r in rows:
        print(f"  {r}")
    return 0


def _publish(args: argparse.Namespace) -> int:
    from . import publish, report
    report.write()
    try:
        print(f"  {publish.publish()}")
    except publish.ScopeMissing:
        print("  The PAT lacks the `gist` scope.\n"
              "  Add it without creating a new token:\n"
              "    1. github.com/settings/tokens  (signed in as vimalyad)\n"
              "    2. open the `oss-pipeline` token\n"
              "    3. tick `gist`, then Update token\n"
              "  The token value does not change, so the Keychain needs no edit.")
        return 1
    return 0


def _replies(args: argparse.Namespace) -> int:
    from . import policy, replies, store
    live = store.by_status(*policy.OPEN_STATUSES)
    any_pending = False
    for cand in live:
        items = replies.pending(cand)
        if not items:
            continue
        any_pending = True
        if args.draft:
            made = replies.refresh_drafts(cand)
            if made:
                print(f"drafted {made} reply(ies) for {cand.slug}\n")
        print(f"=== {cand.repo}#{cand.pr_number} ===")
        for i, item in enumerate(items):
            print(f"\n[{i}] {item.get('cls')} from {item.get('author')} "
                  f"({item.get('kind')})")
            print(f"    they said: {(item.get('body') or '')[:220].strip()}")
            draft = item.get("draft")
            if draft:
                print("\n    DRAFT REPLY:")
                for line in draft.splitlines():
                    print(f"      {line}")
                print(f"\n    post with: pipeline reply {cand.slug} --index {i} --post")
            else:
                print("    (no draft yet -- run `pipeline replies --draft`)")
        print()
    if not any_pending:
        print("no pending feedback")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="pipeline")
    sub = parser.add_subparsers(dest="cmd", required=True)

    v = sub.add_parser("verify", help="verify the identity boundary end to end")
    v.set_defaults(func=_verify)

    d = sub.add_parser("discover", help="sweep the watchlist and write today's report")
    d.add_argument("--repo", action="append", help="limit to these repos (repeatable)")
    d.add_argument("--per-repo", type=int, default=8)
    d.add_argument("--max-harvest", type=int, default=12)
    d.add_argument("--execute", action="store_true", help="not a dry run")
    d.set_defaults(func=_discover)

    a = sub.add_parser("approve", help="approve a proposed candidate")
    a.add_argument("slug")
    a.set_defaults(func=_approve)

    r = sub.add_parser("reject", help="reject a candidate")
    r.add_argument("slug")
    r.add_argument("--reason", required=True)
    r.set_defaults(func=_reject_cmd)

    e = sub.add_parser("exclude", help="never target this repo")
    e.add_argument("repo")
    e.set_defaults(func=_exclude)

    cs = sub.add_parser("cla-signed", help="record a signed CLA, unblocking that repo")
    cs.add_argument("repo")
    cs.set_defaults(func=lambda a: _lines([__import__(
        "oss_pipeline.gate", fromlist=["gate"]).cla_signed(a.repo)]))

    rf = sub.add_parser("refresh-facts", help="re-fetch repo rules for the watchlist")
    rf.set_defaults(func=_refresh_facts)

    rp = sub.add_parser("replies", help="review drafted replies to maintainer feedback")
    rp.add_argument("--draft", action="store_true", help="generate drafts for new items")
    rp.set_defaults(func=_replies)

    po = sub.add_parser("reply", help="post an approved draft reply")
    po.add_argument("slug")
    po.add_argument("--index", type=int, default=0)
    po.add_argument("--post", action="store_true", required=True,
                    help="required: posting is public and irreversible")
    po.set_defaults(func=lambda a: _lines([__import__(
        "oss_pipeline.replies", fromlist=["r"]).post(
            __import__("oss_pipeline.store", fromlist=["s"]).load(a.slug), a.index)]))

    rep = sub.add_parser("report", help="regenerate reports/STATUS.md")
    rep.set_defaults(func=lambda a: _lines(
        [__import__("oss_pipeline.report", fromlist=["r"]).write()]))

    pu = sub.add_parser("publish", help="mirror STATUS.md to a secret gist")
    pu.set_defaults(func=_publish)

    st = sub.add_parser("status", help="what the pipeline is tracking")
    st.set_defaults(func=_status)

    rs = sub.add_parser("rescore", help="re-evaluate stored candidates (no API calls)")
    rs.add_argument("--apply", action="store_true", help="update statuses, not just report")
    rs.set_defaults(func=_rescore)

    im = sub.add_parser("implement", help="build patches for approved candidates")
    im.add_argument("--execute", action="store_true")
    im.add_argument("--limit", type=int, default=1)
    im.set_defaults(func=lambda a: _lines(__import__(
        "oss_pipeline.run", fromlist=["run"]).implement_approved(
            execute=a.execute, limit=a.limit)))

    wt = sub.add_parser("watch", help="poll open PRs for CI/review feedback")
    wt.add_argument("--execute", action="store_true")
    wt.set_defaults(func=lambda a: _lines(__import__(
        "oss_pipeline.run", fromlist=["run"]).watch_open(execute=a.execute)))

    lg = sub.add_parser("ledger", help="write the contribution ledger")
    lg.set_defaults(func=lambda a: _lines(
        [__import__("oss_pipeline.ledger", fromlist=["ledger"]).write()]))

    dy = sub.add_parser("daily", help="one full cycle (what launchd runs)")
    dy.add_argument("--execute", action="store_true")
    dy.set_defaults(func=lambda a: (__import__(
        "oss_pipeline.run", fromlist=["run"]).daily(execute=a.execute), 0)[1])

    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())


def _rescore(args: argparse.Namespace) -> int:
    """Re-evaluate stored candidates against the current scoring config.

    Makes no API calls and no LLM calls -- it reads the briefs already on disk.
    This is the tuning loop: change a bar in policy.yaml, see immediately what
    it would have done, before anything is allowed to write.
    """
    from . import audit, propose, score, store
    from .models import Status

    cands = [c for c in store.all_candidates() if c.brief is not None]
    if not cands:
        print("no harvested candidates to re-score (run `pipeline discover` first)")
        return 1

    touched = score.repos_touched_by_other_accounts()
    would_pass, still_fail = [], []
    for cand in cands:
        ok, fails = score.score(cand, touched=touched)
        (would_pass if ok else still_fail).append((cand, fails))

    print(f"re-scored {len(cands)} harvested candidates\n")
    print(f"WOULD PROPOSE: {len(would_pass)}")
    for cand, _ in would_pass:
        print(f"  {cand.repo}#{cand.issue}  {cand.title[:56]}")
    print(f"\nstill rejected: {len(still_fail)}")
    from collections import Counter
    reasons = Counter(f[0].split("--")[0].strip()[:56] for _, f in still_fail if f)
    for reason, n in reasons.most_common():
        print(f"  {n:3d}  {reason}")

    if not args.apply:
        print("\n(dry run -- pass --apply to update statuses and rewrite the report)")
        return 0

    for cand, fails in would_pass:
        cand.score_failures = []
        cand.reject_reason = ""
        cand.status = Status.PROPOSED       # deliberate override; a tuning action
        cand.history.append({"at": "rescore", "from": "rejected",
                             "to": "proposed", "note": "scorer retuned"})
        store.save(cand)
    for cand, fails in still_fail:
        cand.score_failures = fails
        cand.reject_reason = "; ".join(fails[:2])
        store.save(cand)

    path = propose.write([c for c, _ in would_pass] + [c for c, _ in still_fail])
    audit.record("rescore", detail=f"{len(would_pass)} now proposed", report=str(path))
    print(f"\napplied. report: {path}")
    return 0


def _refresh_facts(args: argparse.Namespace) -> int:
    """Re-fetch repo-level facts for the whole watchlist.

    Worth re-running periodically: projects change their contribution rules, and
    a stale cache is how cli/cli#14448 went out against an issue that project
    does not accept PRs for.
    """
    from . import policy, repofacts, score, store
    from .models import Status

    repos = policy.active_repos()
    print(f"refreshing {len(repos)} repos\n")
    rules = []
    for repo in repos:
        try:
            f = repofacts.facts(repo, refresh=True)
        except Exception as exc:
            print(f"  {repo:44s} FAILED {str(exc)[:50]}")
            continue
        flags = []
        if f.required_issue_labels:
            flags.append(f"requires {f.required_issue_labels}")
        if f.forbidden_issue_labels:
            flags.append(f"forbids {f.forbidden_issue_labels}")
        if f.bans_ai_prs:
            flags.append("BANS AI PRs")
        if f.requires_cla:
            flags.append("CLA")
        print(f"  {repo:44s} {'; '.join(flags) or 'no eligibility rules'}")
        if f.required_issue_labels or f.forbidden_issue_labels:
            rules.append((repo, f))

    print(f"\n{len(rules)} repo(s) state eligibility rules")

    # Re-score everything still in play against the refreshed facts.
    touched = score.repos_touched_by_other_accounts()
    changed = []
    for cand in store.all_candidates():
        if cand.status not in (Status.PROPOSED, Status.APPROVED) or cand.brief is None:
            continue
        cand.facts = None
        ok, fails = score.score(cand, touched=touched)
        if not ok:
            changed.append((cand, fails))
        store.save(cand)

    if changed:
        print(f"\n{len(changed)} in-play candidate(s) NO LONGER PASS:")
        for cand, fails in changed:
            print(f"  {cand.repo}#{cand.issue}: {fails[0][:96]}")
    else:
        print("\nevery in-play candidate still passes")
    return 0
