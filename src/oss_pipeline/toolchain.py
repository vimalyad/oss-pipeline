"""Detect how a repo builds, tests and lints itself, and how to exercise a change.

Two lessons from the first real run, both learned by abandoning a good patch:

1. Running a large repo's ENTIRE suite is the wrong check. It is slow, and it
   fails on problems that have nothing to do with our diff -- pre-existing
   flakes, environment gaps, tests needing hardware we lack. Our job is to show
   *this change* is correct; proving the whole repo green is CI's job, in CI's
   environment.
2. A bare `pytest` misses the project's test plugins. kornia's suite needs
   pytest-timeout; without it the run dies for reasons unrelated to the patch.
   Use the project's own declared dev/test extras.
"""

from __future__ import annotations

import json
import re
import shutil
import subprocess
import tomllib
from pathlib import Path

TEST_EXTRA_NAMES = re.compile(r"^(dev|test|tests|testing|dev-deps|all)$", re.I)


def _python_extras(clone: Path) -> list[str]:
    """Dev/test extras the project declares, so plugins actually get installed."""
    pyproject = clone / "pyproject.toml"
    if not pyproject.exists():
        return []
    try:
        data = tomllib.loads(pyproject.read_text())
    except (tomllib.TOMLDecodeError, OSError):
        return []
    optional = (data.get("project") or {}).get("optional-dependencies") or {}
    groups = (data.get("dependency-groups") or {})
    return [k for k in (*optional, *groups) if TEST_EXTRA_NAMES.match(k)]


def detect(clone: Path) -> dict:
    if (clone / "Cargo.toml").exists():
        return {"name": "cargo", "test": ["cargo test"],
                "lint": ["cargo fmt --check"], "kind": "cargo"}
    if (clone / "go.mod").exists():
        return {"name": "go", "test": ["go test ./..."],
                "lint": ["go vet ./..."], "kind": "go"}
    if (clone / "package.json").exists():
        try:
            scripts = json.loads((clone / "package.json").read_text()).get("scripts", {})
        except (json.JSONDecodeError, OSError):
            scripts = {}
        return {"name": "npm",
                "test": ["npm test"] if "test" in scripts else [],
                "lint": [f"npm run {s}" for s in ("lint", "typecheck") if s in scripts],
                "kind": "npm"}
    if any((clone / m).exists() for m in ("pyproject.toml", "setup.py", "tox.ini")):
        extras = " ".join(f"--extra {e}" for e in _python_extras(clone))
        base = f"uv run {extras} --with pytest".replace("  ", " ")
        return {"name": "pytest", "test": [f"{base} python -m pytest -q"],
                "lint": [], "kind": "pytest", "base": base}
    return {"name": "unknown", "test": [], "lint": [], "kind": "unknown"}


def mirrored_tests(clone: Path, changed: list[str]) -> list[str]:
    """Test files that correspond to changed source files, e.g.
    kornia/core/utils.py -> tests/core/test_utils.py or tests/core/test_helpers.py"""
    # rglob happily returns compiled artefacts; a .pyc is not a test file.
    def usable(rel: str) -> bool:
        return not re.search(r"(^|/)(__pycache__|\.pytest_cache)/|\.pyc$", rel)

    found: set[str] = set()
    for path in changed:
        p = Path(path)
        if not p.suffix:
            continue
        stem = p.stem
        for root in ("tests", "test", "spec", "__tests__"):
            base = clone / root
            if not base.is_dir():
                continue
            # same subpath under the test root, then anything naming the module
            for cand in base.rglob(f"test_{stem}.*"):
                rel = str(cand.relative_to(clone))
                if usable(rel) and is_test_file(rel):
                    found.add(rel)
            parent = p.parent.name
            if parent:
                for cand in base.rglob(f"*{parent}*/test_*.*"):
                    rel = str(cand.relative_to(clone))
                    if usable(rel) and is_test_file(rel):
                        found.add(rel)
    return sorted(found)


DEF_RE = re.compile(r"(?:def|class|func|fn|export function)\s+([A-Za-z_]\w*)")


def _changed_symbols(clone: Path, changed: list[str]) -> list[str]:
    """Symbols the DIFF actually touched, from git's hunk context.

    Scanning whole files and truncating was wrong twice over: it returned every
    symbol in the file, and the alphabetical cut dropped `_torch_svd_cast` --
    the only one that had changed -- so the caller search missed ZCAWhitening
    and CI found the breakage instead. `git diff -U0` puts the enclosing
    function on each @@ header, which is exactly the question being asked.
    """
    names: list[str] = []
    for path in changed:
        if not (clone / path).exists():
            continue
        diff = subprocess.run(
            ["git", "-C", str(clone), "diff", "-U0", "HEAD", "--", path],
            capture_output=True, text=True,
        ).stdout
        if not diff.strip():
            diff = subprocess.run(
                ["git", "-C", str(clone), "diff", "-U0", "origin/HEAD...HEAD", "--", path],
                capture_output=True, text=True,
            ).stdout
        for line in diff.splitlines():
            if line.startswith("@@"):
                ctx = line.split("@@")[-1]
                names += DEF_RE.findall(ctx)
            elif line.startswith(("+", "-")) and not line.startswith(("+++", "---")):
                names += DEF_RE.findall(line[1:])
    return sorted(set(names))


def dependent_tests(clone: Path, changed: list[str], limit: int = 6) -> list[str]:
    """Tests for modules that USE what changed.

    Mapping a changed file to its same-named test is not enough: kornia#4455
    edited kornia/core/utils.py, whose test file passed, while the real breakage
    was in ZCAWhitening -- a different module that merely calls the edited
    function. CI caught it across twelve jobs. Following callers locally is the
    cheap way to catch that class of failure before pushing.
    """
    symbols = [s for s in _changed_symbols(clone, changed) if len(s) > 4][:20]
    if not symbols:
        return []
    try:
        out = subprocess.run(
            ["grep", "-rlE", "|".join(re.escape(s) for s in symbols), "--include=*.py",
             "--include=*.ts", "--include=*.js", "--include=*.go", "--include=*.rs", "."],
            cwd=clone, capture_output=True, text=True, timeout=60,
        ).stdout.split()
    except (subprocess.SubprocessError, OSError):
        return []

    callers = [
        o.lstrip("./") for o in out
        if not re.search(r"(^|/)(tests?|spec|__tests__)/", o.lstrip("./"))
        and o.lstrip("./") not in changed
    ]
    return mirrored_tests(clone, callers)[:limit]


# Living under tests/ does not make a file a test. pytorch/vision keeps every
# binary fixture in test/assets/, so a directory-based filter handed pytest
# `test/assets/damaged_jpeg/TensorFlow-LICENSE` and a JPEG as test targets. A
# test target must look like a test file AND be something a runner can collect.
TEST_FILE_RE = re.compile(
    r"(^|/)(test_[^/]+\.py|[^/]+_test\.py|conftest\.py"
    r"|[^/]+\.(test|spec)\.(js|jsx|ts|tsx|mjs|cjs)"
    r"|[^/]+_test\.go|[^/]+\.rs)$"
)
MAX_TEST_TARGETS = 25


def is_test_file(path: str) -> bool:
    return bool(TEST_FILE_RE.search(path))


def targeted_tests(clone: Path, changed: list[str]) -> tuple[list[str], str]:
    """Command(s) exercising *this change*, plus a human-readable rationale."""
    tc = detect(clone)

    # Go and Rust co-locate tests with the code they cover (foo_test.go beside
    # foo.go; #[cfg(test)] in the module), so scope by the packages the diff
    # touched rather than looking for a separate tests/ tree that will not exist.
    if tc["kind"] == "go":
        pkgs = sorted({str(Path(t).parent) for t in changed if t.endswith(".go")})
        if pkgs:
            return ([f"go test {' '.join('./' + p for p in pkgs)}"],
                    f"targeted packages: {', '.join(pkgs[:4])}")
        return tc["test"], "no Go sources changed; running the project default"
    if tc["kind"] == "cargo":
        return ["cargo test"], "cargo test (crate-scoped)"

    # Python and JS keep tests in their own tree, so find the relevant files.
    touched_tests = [c for c in changed if is_test_file(c)]
    targets = [
        t for t in dict.fromkeys(
            (touched_tests or mirrored_tests(clone, changed))
            + dependent_tests(clone, changed))
        if is_test_file(t)
    ]
    if not targets:
        return tc["test"], "no test files identified; running the project default"

    # A takeover of an old PR merges months of upstream work, so the changed set
    # can be enormous. Running 200 files is neither targeted nor fast; CI covers
    # the rest.
    trimmed = len(targets) - MAX_TEST_TARGETS
    targets = targets[:MAX_TEST_TARGETS]

    if tc["kind"] == "pytest":
        return ([f"{tc['base']} python -m pytest {' '.join(targets)} -q"],
                f"targeted ({len(targets)}"
                + (f", {trimmed} more trimmed" if trimmed > 0 else "")
                + f"): {', '.join(targets[:4])}"
                + (" ..." if len(targets) > 4 else ""))
    return tc["test"], "project default"


# language -> the binary that must exist to build and test it locally.
LANGUAGE_BINARIES = {
    "go": "go", "rust": "cargo", "python": "uv",
    "typescript": "npm", "javascript": "npm",
    "c++": "cmake", "c": "cmake", "java": "mvn", "ruby": "bundle",
}


def missing_toolchain(language: str) -> str | None:
    """The binary this language needs, if it is not installed.

    cli/cli#14386 wrote a correct patch, ran `go test`, and was abandoned on
    "go: command not found" -- Go was not installed, and nine of the watchlist's
    repos are Go. Checking before the work is done turns a wasted implement run
    into an actionable line in the report.
    """
    binary = LANGUAGE_BINARIES.get((language or "").strip().lower())
    if binary and shutil.which(binary) is None:
        return binary
    return None


# An unbuildable environment is not a failing patch. pytorch/vision declares no
# dependencies in pyproject at all -- its CI installs torch from a private index
# and compiles a C++ extension -- so importing it from source raises
# "operator torchvision::nms does not exist" no matter how good the diff is.
# Marking that ABANDONED discards correct work and blames the wrong thing.
ENV_FAILURE_MARKERS = (
    "modulenotfounderror", "importerror while loading conftest",
    "no module named", "does not exist", "command not found",
    "error while finding module", "failed building wheel",
    "could not build wheels", "no matching distribution",
)
# pytest: 2=interrupted, 3=internal error, 4=usage error, 5=nothing collected.
ENV_FAILURE_CODES = {2, 3, 4, 5, 127}


def is_environment_failure(rc: int, output: str) -> bool:
    """True when the run failed to get off the ground, rather than failing."""
    if rc in ENV_FAILURE_CODES:
        return True
    low = (output or "").lower()
    return any(m in low for m in ENV_FAILURE_MARKERS)
