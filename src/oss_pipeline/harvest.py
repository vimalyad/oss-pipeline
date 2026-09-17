"""Phase C: the full issue thread, verbatim.

Maintainers bury the real requirements mid-thread -- the desired approach, the
approaches already rejected, the constraint that decides whether a patch is
merged or closed. Missing one of those produces a confidently wrong PR, which is
worse than no PR because it wastes a maintainer's time and reads as careless.

So nothing is discarded: complete payloads land in state/context/<slug>.raw.json
and the distilled brief (brief.py) is derived from them. The raw file is what you
read when you want to check the brief's reading rather than trust it.
"""

from __future__ import annotations

import json

from . import ghapi, store
from .models import Candidate

THREAD = """
query($owner:String!, $name:String!, $number:Int!) {
  repository(owner:$owner, name:$name) {
    nameWithOwner stargazerCount
    primaryLanguage { name }
    issue(number:$number) {
      number title url body createdAt updatedAt
      author { login } authorAssociation
      reactions { totalCount }
      labels(first:30) { nodes { name } }
      comments(first:100) {
        totalCount
        nodes {
          url createdAt body authorAssociation
          author { login }
          reactions { totalCount }
        }
      }
      timelineItems(first:100, itemTypes:[
        LABELED_EVENT, UNLABELED_EVENT, ASSIGNED_EVENT, UNASSIGNED_EVENT,
        CLOSED_EVENT, REOPENED_EVENT, CROSS_REFERENCED_EVENT,
        MARKED_AS_DUPLICATE_EVENT, LOCKED_EVENT, RENAMED_TITLE_EVENT
      ]) {
        nodes {
          __typename
          ... on LabeledEvent { createdAt label { name } actor { login } }
          ... on UnlabeledEvent { createdAt label { name } actor { login } }
          ... on AssignedEvent { createdAt actor { login } }
          ... on ClosedEvent { createdAt actor { login } }
          ... on ReopenedEvent { createdAt actor { login } }
          ... on LockedEvent { createdAt actor { login } lockReason }
          ... on MarkedAsDuplicateEvent { createdAt actor { login } }
          ... on RenamedTitleEvent { createdAt previousTitle currentTitle }
          ... on CrossReferencedEvent {
            createdAt willCloseTarget
            source {
              __typename
              ... on PullRequest { number url state merged isDraft author { login } }
              ... on Issue { number url state }
            }
          }
        }
      }
    }
  }
}
"""

PR_THREAD = """
query($owner:String!, $name:String!, $number:Int!) {
  repository(owner:$owner, name:$name) {
    pullRequest(number:$number) {
      number url state isDraft body createdAt updatedAt
      author { login }
      additions deletions changedFiles
      commits(last:20) { nodes { commit { oid messageHeadline committedDate author { user { login } } } } }
      reviews(first:50) {
        nodes {
          state createdAt body authorAssociation
          author { login }
          comments(first:50) { nodes { path line body createdAt url } }
        }
      }
      comments(first:50) {
        nodes { createdAt body authorAssociation author { login } url }
      }
      files(first:100) { nodes { path additions deletions } }
    }
  }
}
"""


def harvest(cand: Candidate) -> dict:
    """Fetch and persist everything. Returns the raw payload."""
    owner, name = cand.repo.split("/")
    payload: dict = {"issue": None, "linked_prs": []}

    data = ghapi.graphql(THREAD, owner=owner, name=name, number=cand.issue)
    repo_node = data["repository"]
    payload["repo"] = {
        "nameWithOwner": repo_node["nameWithOwner"],
        "stars": repo_node["stargazerCount"],
        "language": (repo_node.get("primaryLanguage") or {}).get("name", ""),
    }
    payload["issue"] = repo_node["issue"]

    # Linked PRs matter for two reasons: a stale one may be worth taking over,
    # and its review comments are often where the maintainer stated the spec.
    seen: set[int] = set()
    for item in payload["issue"]["timelineItems"]["nodes"]:
        src = (item or {}).get("source") or {}
        if src.get("__typename") != "PullRequest" or src["number"] in seen:
            continue
        seen.add(src["number"])
        try:
            pr = ghapi.graphql(
                PR_THREAD, owner=owner, name=name, number=src["number"]
            )["repository"]["pullRequest"]
            payload["linked_prs"].append(pr)
        except ghapi.GhError as exc:
            payload["linked_prs"].append(
                {"number": src["number"], "error": str(exc)[:200]}
            )

    store.CONTEXT.mkdir(parents=True, exist_ok=True)
    out = store.CONTEXT / f"{cand.slug}.raw.json"
    out.write_text(json.dumps(payload, indent=2))
    return payload


def raw_path(cand: Candidate):
    return store.CONTEXT / f"{cand.slug}.raw.json"


def render_thread(payload: dict, max_chars: int = 60_000) -> str:
    """Flatten the payload into the plain transcript the brief is extracted from.

    Author association is kept on every line: an OWNER/MEMBER statement is the
    spec, a drive-by NONE comment is an opinion, and collapsing that distinction
    is how a pipeline ends up implementing a random stranger's suggestion.
    """
    iss = payload["issue"]
    lines = [
        f"REPO: {payload['repo']['nameWithOwner']} "
        f"({payload['repo']['language']}, {payload['repo']['stars']} stars)",
        f"ISSUE #{iss['number']}: {iss['title']}",
        f"URL: {iss['url']}",
        f"LABELS: {', '.join(l['name'] for l in iss['labels']['nodes'])}",
        "",
        f"--- opened by {(iss.get('author') or {}).get('login','?')} "
        f"[{iss['authorAssociation']}] at {iss['createdAt']} ---",
        (iss.get("body") or "").strip(),
        "",
    ]
    for c in iss["comments"]["nodes"]:
        lines += [
            f"--- comment by {(c.get('author') or {}).get('login','?')} "
            f"[{c['authorAssociation']}] at {c['createdAt']} "
            f"({c['reactions']['totalCount']} reactions) {c['url']} ---",
            (c.get("body") or "").strip(),
            "",
        ]

    for pr in payload.get("linked_prs", []):
        if pr.get("error"):
            continue
        lines += [
            f"=== LINKED PR #{pr['number']} ({pr['state']}"
            f"{', draft' if pr.get('isDraft') else ''}) by "
            f"{(pr.get('author') or {}).get('login','?')} "
            f"+{pr.get('additions',0)}/-{pr.get('deletions',0)} "
            f"across {pr.get('changedFiles',0)} files ===",
            (pr.get("body") or "").strip()[:2000],
            "",
        ]
        for rv in pr.get("reviews", {}).get("nodes", []):
            lines.append(
                f"--- PR review [{rv['state']}] by "
                f"{(rv.get('author') or {}).get('login','?')} "
                f"[{rv['authorAssociation']}] at {rv['createdAt']} ---"
            )
            if (rv.get("body") or "").strip():
                lines.append(rv["body"].strip())
            for rc in rv.get("comments", {}).get("nodes", []):
                lines.append(f"  [{rc['path']}:{rc.get('line')}] {rc['body'].strip()}")
            lines.append("")

    text = "\n".join(lines)
    if len(text) > max_chars:
        head = text[: max_chars // 2]
        tail = text[-max_chars // 2:]
        text = head + "\n\n[... middle of thread truncated ...]\n\n" + tail
    return text
