"""Interface seams. This is where SOLID applies to *our* code.

Each stage depends on these Protocols rather than on a concrete module, so
discovery can move from GraphQL polling to webhooks, or the runner from launchd
to GitHub Actions, without the state machine noticing.
"""

from __future__ import annotations

from pathlib import Path
from typing import Protocol, Sequence

from .models import Brief, Candidate, Contest, PRSignal, RepoFacts


class Discoverer(Protocol):
    def discover(self, repos: Sequence[str]) -> list[Candidate]: ...


class Contester(Protocol):
    def classify(self, cand: Candidate) -> tuple[Contest, PRSignal | None]: ...


class Harvester(Protocol):
    def harvest(self, cand: Candidate) -> Path: ...
    def brief(self, cand: Candidate, raw: Path) -> Brief: ...


class RepoInspector(Protocol):
    def facts(self, repo: str) -> RepoFacts: ...


class Scorer(Protocol):
    def score(self, cand: Candidate) -> tuple[bool, list[str]]: ...


class Implementer(Protocol):
    def implement(self, cand: Candidate) -> Path: ...


class Submitter(Protocol):
    def submit(self, cand: Candidate, clone: Path) -> str: ...


class Watcher(Protocol):
    def poll(self, cand: Candidate) -> list[str]: ...
