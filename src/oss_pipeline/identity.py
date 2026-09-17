"""The identity boundary.

Everything this pipeline does in public happens under the ``vimalyad`` account.
This machine's ambient git/gh configuration resolves to the *work* account --
global ``user.email`` is that account's noreply address, and the
global credential helper is ``gh auth git-credential``, which serves the work
token to any HTTPS push. Identity here is therefore never *inherited*; it is
asserted per clone, before any commit or push.

Four independent layers (plan section 1). This module owns 1-3 and re-asserts 4,
because ``core.hooksPath`` can be clobbered by a repo's own tooling -- husky does
exactly this on ``npm install`` -- which would silently disarm the git hooks.
Never trust the hooks alone; ``assert_clone`` runs in-process before every push.
"""

from __future__ import annotations

import os
import shlex
import subprocess
from dataclasses import dataclass
from pathlib import Path

CREDENTIAL_KEY = "credential.https://github.com.helper"

ROOT = Path(__file__).resolve().parents[2]


class IdentityError(RuntimeError):
    """A clone is not provably operating as the OSS identity.

    Raised with *every* violation found, not just the first: when this fires
    during an unattended run the whole picture is what makes it debuggable.
    """


@dataclass(frozen=True)
class Identity:
    login: str
    name: str
    email: str
    keychain_account: str
    keychain_service: str
    work_login: str
    work_email: str
    other_logins: tuple[str, ...] = ()
    other_emails: tuple[str, ...] = ()
    root: Path = ROOT

    @property
    def helper_path(self) -> Path:
        return self.root / "bin" / "gh-token-helper"

    @property
    def hooks_path(self) -> Path:
        return self.root / "githooks"


def load_identity(env_file: Path | None = None) -> Identity:
    """Parse ``config/identity.env``.

    Deliberately a hand-rolled parser rather than sourcing the file through a
    shell: this is the security-critical path and it must not execute anything.
    The same file is read by the git hooks, which have no interpreter available
    beyond ``sh`` -- hence the KEY="value" format rather than YAML.
    """
    path = env_file or ROOT / "config" / "identity.env"
    values: dict[str, str] = {}
    for raw in path.read_text().splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, _, val = line.partition("=")
        values[key.strip()] = shlex.split(val)[0] if val.strip() else ""

    try:
        return Identity(
            login=values["OSS_LOGIN"],
            name=values["OSS_NAME"],
            email=values["OSS_EMAIL"],
            keychain_account=values["OSS_KEYCHAIN_ACCOUNT"],
            keychain_service=values["OSS_KEYCHAIN_SERVICE"],
            work_login=values["WORK_LOGIN"],
            work_email=values["WORK_EMAIL"],
            other_logins=tuple(values.get("OTHER_LOGINS", "").split()),
            other_emails=tuple(values.get("OTHER_EMAILS", "").split()),
            root=path.resolve().parents[1],
        )
    except KeyError as exc:
        raise IdentityError(f"{path} is missing {exc.args[0]}") from exc


def token(ident: Identity | None = None) -> str:
    """Read the PAT from the macOS Keychain. Never cached, never written down."""
    ident = ident or load_identity()
    proc = subprocess.run(
        ["security", "find-generic-password",
         "-a", ident.keychain_account, "-s", ident.keychain_service, "-w"],
        capture_output=True, text=True,
    )
    if proc.returncode != 0:
        raise IdentityError(
            f"No Keychain item {ident.keychain_service}/{ident.keychain_account}. "
            f"Create it with:\n"
            f"  security add-generic-password -a {ident.keychain_account} "
            f"-s {ident.keychain_service} -w"
        )
    return proc.stdout.strip()


def pipeline_env(ident: Identity | None = None) -> dict[str, str]:
    """Environment for every subprocess that talks to GitHub or writes commits.

    ``GH_TOKEN`` takes precedence over gh's keyring, so this overrides the
    machine's active account per-process. That is the whole reason
    ``gh auth switch`` is banned: it mutates global state a scheduled job cannot
    reason about, and the user switches accounts by hand in parallel.
    """
    ident = ident or load_identity()
    env = dict(os.environ)
    env.update(
        GH_TOKEN=token(ident),
        GIT_AUTHOR_NAME=ident.name,
        GIT_AUTHOR_EMAIL=ident.email,
        GIT_COMMITTER_NAME=ident.name,
        GIT_COMMITTER_EMAIL=ident.email,
        GIT_TERMINAL_PROMPT="0",
    )
    env.pop("GITHUB_TOKEN", None)  # would otherwise race with GH_TOKEN
    return env


def _git(clone: Path, *args: str, check: bool = True) -> str:
    proc = subprocess.run(
        ["git", "-C", str(clone), *args],
        capture_output=True, text=True,
    )
    if check and proc.returncode != 0:
        raise IdentityError(
            f"git {' '.join(args)} failed in {clone}: {proc.stderr.strip()}"
        )
    return proc.stdout.strip()


def harden_clone(clone: Path, ident: Identity | None = None) -> None:
    """Apply layers 2-4 to a fresh clone. Must run before the first commit."""
    ident = ident or load_identity()

    _git(clone, "config", "--local", "user.name", ident.name)
    _git(clone, "config", "--local", "user.email", ident.email)

    # The empty value resets the helper chain, discarding the global
    # `gh auth git-credential` that would otherwise serve the work token.
    # Order matters: git reads system -> global -> local, so a local reset
    # clears everything ahead of it.
    _git(clone, "config", "--local", "--replace-all", CREDENTIAL_KEY, "")
    _git(clone, "config", "--local", "--add", CREDENTIAL_KEY, str(ident.helper_path))

    # One canonical hooks directory rather than copies per clone: no drift, and
    # it also means we do not execute an untrusted repo's own hooks.
    _git(clone, "config", "--local", "core.hooksPath", str(ident.hooks_path))

    exclude_agent_scaffolding(clone)


# Agent scaffolding the implementer may create in a clone. Several repos ship
# their own `.claude/skills/...`, and an auto-fix run has twice produced a copy
# under `.agents/` -- which `git add -A` would then commit into a public PR.
#
# The submit guard catches it, but catching is weaker than preventing: listing
# these in the clone's private exclude file means `git add -A` never stages
# them in the first place. The guard stays as the second layer.
AGENT_SCAFFOLDING = (".agents/", ".aider*", ".cursor/", ".codex/")


def exclude_agent_scaffolding(clone: Path) -> None:
    """Make agent scaffolding unstageable in this clone.

    Written to .git/info/exclude rather than .gitignore: it is our local
    concern, and modifying a repo's tracked .gitignore would show up in the
    diff we are about to ask someone to merge.
    """
    path = clone / ".git" / "info" / "exclude"
    path.parent.mkdir(parents=True, exist_ok=True)
    existing = path.read_text() if path.exists() else ""
    missing = [p for p in AGENT_SCAFFOLDING if p not in existing]
    if not missing:
        return
    header = "" if existing.endswith("\n") or not existing else "\n"
    path.write_text(existing + header
                    + "# added by oss-pipeline: never stage agent scaffolding\n"
                    + "\n".join(missing) + "\n")


def effective_credential_helpers(clone: Path) -> list[str]:
    """The helper chain git will actually consult, after reset semantics.

    An empty entry clears every helper declared before it, so only the entries
    following the last empty one are live.
    """
    out = _git(clone, "config", "--get-all", CREDENTIAL_KEY, check=False)
    values = out.split("\n") if out else []
    resets = [i for i, v in enumerate(values) if v == ""]
    return values[resets[-1] + 1:] if resets else values


def assert_clone(clone: Path, ident: Identity | None = None) -> None:
    """Pre-flight check. Called before every commit and every push.

    Independent of the git hooks by design -- this is what still holds when a
    repo's tooling has rewritten ``core.hooksPath`` out from under us.
    """
    ident = ident or load_identity()
    problems: list[str] = []

    name = _git(clone, "config", "--local", "user.name", check=False)
    email = _git(clone, "config", "--local", "user.email", check=False)
    if email != ident.email:
        hint = ""
        for login, alt in zip(ident.other_logins, ident.other_emails):
            if email == alt:
                which = "WORK" if login == ident.work_login else "another of your"
                hint = f" (this is the {which} account {login})"
                break
        problems.append(f"local user.email is {email!r}, expected {ident.email!r}{hint}")
    if name != ident.name:
        problems.append(f"local user.name is {name!r}, expected {ident.name!r}")

    helpers = effective_credential_helpers(clone)
    if helpers != [str(ident.helper_path)]:
        problems.append(
            f"credential helper chain is {helpers!r}, expected "
            f"[{str(ident.helper_path)!r}] -- the work token could be served"
        )

    hooks = _git(clone, "config", "--local", "core.hooksPath", check=False)
    if hooks != str(ident.hooks_path):
        problems.append(
            f"core.hooksPath is {hooks!r}, expected {str(ident.hooks_path)!r} "
            f"-- layer 4 guards are disarmed (did a package manager rewrite it?)"
        )

    for remote in _git(clone, "remote", check=False).split("\n"):
        if not remote:
            continue
        url = _git(clone, "remote", "get-url", "--push", remote, check=False)
        if remote == "fork" and f"/{ident.login}/" not in url and f":{ident.login}/" not in url:
            problems.append(f"remote {remote!r} push url {url!r} is not a {ident.login} fork")

    if problems:
        raise IdentityError(
            f"{clone} is not provably the OSS identity:\n"
            + "\n".join(f"  - {p}" for p in problems)
        )


def assert_token_account(ident: Identity | None = None) -> str:
    """Confirm the Keychain token really belongs to the OSS account."""
    ident = ident or load_identity()
    proc = subprocess.run(
        ["gh", "api", "user", "--jq", ".login"],
        capture_output=True, text=True, env=pipeline_env(ident),
    )
    if proc.returncode != 0:
        raise IdentityError(f"gh api user failed: {proc.stderr.strip()}")
    login = proc.stdout.strip()
    if login != ident.login:
        raise IdentityError(
            f"Keychain token authenticates as {login!r}, expected {ident.login!r}. "
            f"Refusing to continue."
        )
    return login
