"""Tests for the identity boundary.

The critical one is `test_global_work_helper_is_not_consulted`: this machine has
`credential.https://github.com.helper = !gh auth git-credential` set GLOBALLY,
which serves the *work* account token to any HTTPS push. Layer 2 relies on a
local empty helper entry resetting that chain. If git's reset semantics ever
change, that test fails loudly rather than the pipeline quietly pushing as the
wrong account.
"""

from __future__ import annotations

import dataclasses
import os
import re
import shutil
import subprocess
from pathlib import Path

import pytest

from oss_pipeline.identity import (
    CREDENTIAL_KEY,
    IdentityError,
    assert_clone,
    effective_credential_helpers,
    harden_clone,
    load_identity,
)

SENTINEL = "sentinel-token-not-a-real-pat"


@pytest.fixture
def ident():
    return load_identity()


@pytest.fixture
def stub_root(tmp_path: Path, ident):
    """A fake pipeline root whose credential helper emits a known sentinel.

    Lets us prove which helper git actually consults without touching the real
    Keychain (and without a GUI keychain prompt hanging an unattended test).
    """
    (tmp_path / "bin").mkdir()
    (tmp_path / "githooks").mkdir()
    helper = tmp_path / "bin" / "gh-token-helper"
    helper.write_text(
        "#!/bin/sh\n"
        '[ "$1" = get ] || exit 0\n'
        "while IFS= read -r l; do [ -z \"$l\" ] && break; done\n"
        f'printf "username=%s\\n" "{ident.login}"\n'
        f'printf "password=%s\\n" "{SENTINEL}"\n'
    )
    helper.chmod(0o700)
    return tmp_path


@pytest.fixture
def clone(tmp_path: Path, stub_root, ident):
    repo = tmp_path / "repo"
    repo.mkdir()
    subprocess.run(["git", "init", "-q", str(repo)], check=True)
    harden_clone(repo, dataclasses.replace(ident, root=stub_root))
    return repo


def test_harden_sets_local_identity(clone, ident):
    def cfg(key):
        return subprocess.run(
            ["git", "-C", str(clone), "config", "--local", key],
            capture_output=True, text=True,
        ).stdout.strip()

    assert cfg("user.email") == ident.email
    assert cfg("user.name") == ident.name


def test_global_work_helper_is_present_but_reset(clone, stub_root):
    """The global work helper is visible in merged config, yet inert."""
    merged = subprocess.run(
        ["git", "-C", str(clone), "config", "--get-all", CREDENTIAL_KEY],
        capture_output=True, text=True,
    ).stdout.split("\n")

    assert any("gh auth git-credential" in v for v in merged), (
        "expected the machine's global gh helper in merged config; "
        "if this fails the test is no longer proving anything"
    )
    assert effective_credential_helpers(clone) == [
        str(stub_root / "bin" / "gh-token-helper")
    ]


def test_global_work_helper_is_not_consulted(clone):
    """Decisive: git must return OUR token, never the work account's.

    Runs with the REAL environment on purpose -- real HOME so the global
    `gh auth git-credential` is in the merged config, and real PATH so `gh` is
    actually executable. If the local reset failed, git would fall through to
    that helper and hand back the work token, which is precisely what the second
    assertion catches. Anything token-shaped is redacted before it can reach a
    failure message or CI log.
    """
    env = dict(os.environ)
    env["GIT_TERMINAL_PROMPT"] = "0"

    proc = subprocess.run(
        ["git", "-C", str(clone), "credential", "fill"],
        input="protocol=https\nhost=github.com\n\n",
        capture_output=True, text=True, env=env,
    )
    combined = proc.stdout + proc.stderr
    redacted = re.sub(r"(gh[pousr]_)[A-Za-z0-9]+", r"\1<redacted>", combined)

    assert shutil.which("gh", path=env.get("PATH")), (
        "gh must be on PATH or the global helper could not have answered, "
        "making this test prove nothing"
    )
    assert f"password={SENTINEL}" in proc.stdout, redacted
    assert not re.search(r"gh[pousr]_[A-Za-z0-9]", combined), (
        "WORK account token leaked into a hardened clone: " + redacted
    )


def test_assert_clone_passes_when_hardened(clone, stub_root, ident):
    assert_clone(clone, dataclasses.replace(ident, root=stub_root))


def test_assert_clone_rejects_work_email(clone, stub_root, ident):
    subprocess.run(
        ["git", "-C", str(clone), "config", "--local", "user.email", ident.work_email],
        check=True,
    )
    with pytest.raises(IdentityError, match="WORK account"):
        assert_clone(clone, dataclasses.replace(ident, root=stub_root))


def test_assert_clone_rejects_clobbered_hookspath(clone, stub_root, ident):
    """Simulates husky's `git config core.hooksPath .husky/_` on npm install."""
    subprocess.run(
        ["git", "-C", str(clone), "config", "--local", "core.hooksPath", ".husky/_"],
        check=True,
    )
    with pytest.raises(IdentityError, match="layer 4 guards are disarmed"):
        assert_clone(clone, dataclasses.replace(ident, root=stub_root))


def test_assert_clone_rejects_hijacked_credential_helper(clone, stub_root, ident):
    """An extra helper appended after ours could answer first."""
    subprocess.run(
        ["git", "-C", str(clone), "config", "--local", "--add",
         CREDENTIAL_KEY, "!gh auth git-credential"],
        check=True,
    )
    with pytest.raises(IdentityError, match="work token could be served"):
        assert_clone(clone, dataclasses.replace(ident, root=stub_root))


def test_assert_clone_rejects_non_fork_push_remote(clone, stub_root, ident):
    subprocess.run(
        ["git", "-C", str(clone), "remote", "add", "fork",
         "https://github.com/facebook/react.git"],
        check=True,
    )
    with pytest.raises(IdentityError, match="not a vimalyad fork"):
        assert_clone(clone, dataclasses.replace(ident, root=stub_root))


def test_leak_detector_is_not_vacuous(tmp_path, ident):
    """Meta-test: prove a clone WITHOUT the reset really does leak the work token.

    Without this, `test_global_work_helper_is_not_consulted` could pass for the
    wrong reason -- e.g. the machine's global helper removed, or `gh` off PATH --
    and we would have a green safety test that cannot detect a leak.

    If this one fails, the environment changed and the decisive test above may
    have become vacuous. Investigate before trusting it.
    """
    naive = tmp_path / "naive"
    naive.mkdir()
    subprocess.run(["git", "init", "-q", str(naive)], check=True)
    # our helper appended, but the global chain NOT reset -- the bug we defend against
    subprocess.run(
        ["git", "-C", str(naive), "config", "--local", "--add",
         CREDENTIAL_KEY, str(ident.helper_path)],
        check=True,
    )

    env = dict(os.environ)
    env["GIT_TERMINAL_PROMPT"] = "0"
    proc = subprocess.run(
        ["git", "-C", str(naive), "credential", "fill"],
        input="protocol=https\nhost=github.com\n\n",
        capture_output=True, text=True, env=env,
    )

    assert f"username={ident.work_login}" in proc.stdout, (
        "expected the un-reset clone to serve the WORK account; the decisive "
        "isolation test may no longer be proving anything. Got: "
        + re.sub(r"(gh[pousr]_)[A-Za-z0-9]+", r"\1<redacted>", proc.stdout)
    )


@pytest.mark.parametrize("alt_index", [0, 1])
def test_assert_clone_rejects_every_alt_account(clone, stub_root, ident, alt_index):
    """All three accounts share the display name, so each alt must be named.

    Not a security boundary -- anything != OSS_EMAIL is already blocked -- but a
    blocked unattended push must say *which* identity it caught.
    """
    alt_email = ident.other_emails[alt_index]
    alt_login = ident.other_logins[alt_index]
    subprocess.run(
        ["git", "-C", str(clone), "config", "--local", "user.email", alt_email],
        check=True,
    )
    with pytest.raises(IdentityError, match=alt_login):
        assert_clone(clone, dataclasses.replace(ident, root=stub_root))
