"""Phase A: cheap metadata sweep over the tier-1 watchlist.

One GraphQL search per repo with the labels OR'd together (comma-separated
qualifier values are a union in GitHub search syntax), rather than one query per
repo/label pair. Bodies and comment threads are deliberately NOT fetched here --
that is Phase C, and it only runs on candidates that survive the cheap bars.
"""

from __future__ import annotations

from datetime import datetime, timezone

from . import audit, ghapi, policy, store
from .models import Candidate

SEARCH = """
query($q:String!, $n:Int!) {
  search(query:$q, type:ISSUE, first:$n) {
    issueCount
    nodes {
      ... on Issue {
        number title url createdAt updatedAt
        repository { nameWithOwner stargazerCount primaryLanguage { name } }
        author { login }
        labels(first:20) { nodes { name } }
        comments { totalCount }
        reactions { totalCount }
        timelineItems(first:30, itemTypes:[CROSS_REFERENCED_EVENT]) {
          nodes {
            ... on CrossReferencedEvent {
              willCloseTarget
              source {
                __typename
                ... on PullRequest {
                  number url state isDraft updatedAt closed merged
                  author { login }
                }
              }
            }
          }
        }
      }
    }
  }
}
"""


def _query_for(repo: str, labels: list[str]) -> str:
    """Search the generic labels PLUS whatever this repo requires.

    eslint accepts outside PRs only for issues labelled `accepted`, and
    typescript-eslint only for `accepting prs` -- neither of which is in the
    generic list. Without this, the only issues we could ever find in those
    repos were ones their own rules make ineligible: structurally dead targets.
    """
    wanted = list(labels)
    facts = store.load_repo_facts(repo)
    if facts:
        for required in facts.required_issue_labels or []:
            if required.lower() not in {w.lower() for w in wanted}:
                wanted.append(required)
    quoted = ",".join(f'"{l}"' for l in wanted)
    return (
        f"repo:{repo} is:issue is:open no:assignee "
        f"label:{quoted} sort:updated-desc"
    )


def linked_prs(node: dict) -> list[dict]:
    """Open PRs that cross-reference this issue."""
    out = []
    for item in node.get("timelineItems", {}).get("nodes") or []:
        src = (item or {}).get("source") or {}
        if src.get("__typename") != "PullRequest":
            continue
        if src.get("state") != "OPEN":
            continue
        out.append(src)
    return out


def discover(repos: list[str] | None = None, per_repo: int = 10) -> list[Candidate]:
    """Sweep every watchlist repo, then take candidates round-robin.

    The cap must not truncate the sweep sequentially: doing so means the tail of
    the watchlist is never examined at all, and the repos at the end are dead
    weight. Every repo is queried, then candidates are interleaved so the cap
    trims breadth-first rather than amputating the alphabet.
    """
    repos = repos if repos is not None else policy.active_repos()
    labels = policy.labels()
    cfg = policy.policy()
    cap = cfg["caps"]["max_candidates_per_run"]
    recheck = cfg["caps"].get("reconsider_rejected_after_days", 14)

    per_repo_found: list[list[Candidate]] = []
    for repo in repos:
        try:
            data = ghapi.graphql(SEARCH, q=_query_for(repo, labels), n=per_repo)
        except ghapi.GhError as exc:
            print(f"  {repo:45s} skipped ({str(exc)[:60]})")
            continue

        nodes = [n for n in data["search"]["nodes"] if n]
        bucket: list[Candidate] = []
        for n in nodes:
            repo_name = n["repository"]["nameWithOwner"]
            cand = Candidate(
                repo=repo_name,
                issue=n["number"],
                title=n["title"],
                url=n["url"],
                labels=[l["name"] for l in n["labels"]["nodes"]],
                comments=n["comments"]["totalCount"],
                reactions=n["reactions"]["totalCount"],
                issue_updated_at=n["updatedAt"],
                issue_created_at=n["createdAt"],
            )
            # Transient rejections (claimed, contested, unconverged) expire and
            # get another look; structural ones and human rejections do not.
            if not store.should_reconsider(cand.slug, after_days=recheck):
                continue
            cand._linked_prs = linked_prs(n)   # consumed by contest.py this run
            bucket.append(cand)
        print(f"  {repo:45s} {len(nodes):3d} open  {len(bucket):3d} new")
        if bucket:
            per_repo_found.append(bucket)

    # Round-robin: one from each repo, then a second from each, and so on.
    found: list[Candidate] = []
    for depth in range(per_repo):
        for bucket in per_repo_found:
            if depth < len(bucket) and len(found) < cap:
                found.append(bucket[depth])
        if len(found) >= cap:
            break

    spread = len({c.repo for c in found})
    print(f"\n  {len(found)} candidates across {spread} repos "
          f"(cap {cap}, swept {len(repos)})")
    audit.record("discover",
                 detail=f"{len(found)} candidates across {spread}/{len(repos)} repos")
    return found
