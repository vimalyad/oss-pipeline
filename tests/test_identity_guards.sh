#!/bin/bash
# Proves the Layer-3/Layer-4 identity guards abort when they should, and do
# NOT fire on legitimate messages. Runs fully offline: no token, no network.
# Re-run after any change to githooks/ or config/forbidden-trailers.txt.
set -uo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
. "$ROOT/config/identity.env"

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
PASS=0; FAIL=0

ok()   { printf '  \033[32mPASS\033[0m  %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; FAIL=$((FAIL+1)); }
check(){ # check <desc> <expected: block|allow> <actual exit code>
    if [ "$2" = block ] && [ "$3" -ne 0 ]; then ok "$1"
    elif [ "$2" = allow ] && [ "$3" -eq 0 ]; then ok "$1"
    else bad "$1 (expected $2, exit=$3)"; fi
}

# ---------------------------------------------------------------- fixture ---
REPO="$TMP/repo"
git init -q "$REPO"; cd "$REPO"
git config core.hooksPath "$ROOT/githooks"
git config user.name  "$OSS_NAME"
git config user.email "$OSS_EMAIL"
echo hello > f.txt; git add f.txt

echo "== commit-msg guard =="

git commit -q -m "feat: add f" 2>/dev/null
check "clean message commits" allow $?

# false-positive guard: legitimate brand mentions must NOT be blocked
for msg in \
  "fix: handle anthropic SDK timeout on retry" \
  "refactor: rename claude_client to llm_client" \
  "docs: document the Claude API rate limits" \
  "chore: regenerated with updated protobuf schema"
do
    echo "$RANDOM" >> f.txt; git add f.txt
    git commit -q -m "$msg" 2>/dev/null
    check "allows legit: \"${msg:0:42}...\"" allow $?
done

# true positives: attribution constructs must be blocked.
# Array (not a heredoc) so multi-line messages keep their embedded newlines.
BAD_MSGS=(
  $'fix: thing\n\nCo-Authored-By: Claude <noreply@anthropic.com>'
  $'feat: add parser\n\nGenerated with Claude Code'
  $'feat: z\n\nGenerated with [Claude Code](https://claude.com/claude-code)'
  $'fix: y\n\nAssisted-by: Cursor'
  $'chore: bump deps \xf0\x9f\xa4\x96'
  'fix: edge case, written by Claude'
  'feat: ai-generated test coverage'
  'refactor: cleanup with AI assistance'
)
for msg in "${BAD_MSGS[@]}"; do
    echo "$RANDOM" >> f.txt; git add f.txt
    git commit -q -m "$msg" 2>/dev/null
    rc=$?
    first=$(printf '%s' "$msg" | head -1)
    check "blocks: \"${first:0:40}\"" block $rc
    git reset -q HEAD f.txt 2>/dev/null; git checkout -q -- f.txt 2>/dev/null || true
done

# Issue references must NOT be blocked. The patterns file is passed to grep -f,
# which reads its "#" comment lines as patterns too -- a bare "#" matched every
# message containing "Fixes #1". Caught only in the first live run.
for msg in "fix: thing

Fixes #1" "feat: x (#4321)" "chore: bump deps

Closes #99, refs #100"
do
    echo "$RANDOM" >> f.txt; git add f.txt
    git commit -q -m "$msg" 2>/dev/null
    first=$(printf '%s' "$msg" | head -1)
    check "allows issue ref: \"${first:0:34}\"" allow $?
done

# DCO sign-off by a human must NOT be blocked (required by many repos)
echo "$RANDOM" >> f.txt; git add f.txt
git commit -q -m "fix: thing

Signed-off-by: $OSS_NAME <$OSS_EMAIL>" 2>/dev/null
check "allows human DCO Signed-off-by" allow $?

echo; echo "== pre-push guard =="
echo final >> f.txt; git add f.txt; git commit -q -m "chore: final"
SHA=$(git rev-parse HEAD)
ZERO=0000000000000000000000000000000000000000
push() { printf 'refs/heads/main %s refs/heads/main %s\n' "$SHA" "$ZERO" \
           | "$ROOT/githooks/pre-push" "$1" "$2" >/dev/null 2>&1; }

push fork "https://github.com/$OSS_LOGIN/somerepo.git"
check "allows push to own fork" allow $?

push origin "https://github.com/facebook/react.git"
check "blocks push to upstream (not our fork)" block $?

push fork "https://github.com/$WORK_LOGIN/somerepo.git"
check "blocks push to WORK account fork" block $?

git config user.email "wrong@example.com"
push fork "https://github.com/$OSS_LOGIN/somerepo.git"
check "blocks wrong user.email" block $?

git config user.email "$WORK_EMAIL"
push fork "https://github.com/$OSS_LOGIN/somerepo.git"
check "blocks WORK user.email" block $?
git config user.email "$OSS_EMAIL"

# a commit authored by the wrong identity, sneaked past commit-msg
echo sneak >> f.txt; git add f.txt
GIT_AUTHOR_EMAIL="$WORK_EMAIL" GIT_AUTHOR_NAME="$WORK_LOGIN" \
  git commit -q -m "chore: sneaky"
SHA=$(git rev-parse HEAD)
push fork "https://github.com/$OSS_LOGIN/somerepo.git"
check "blocks commit authored by WORK identity" block $?

# attribution committed with --no-verify must still be caught at push
git reset -q --hard HEAD~1
echo bypass >> f.txt; git add f.txt
git commit -q --no-verify -m "fix: x

Co-Authored-By: Claude <noreply@anthropic.com>"
SHA=$(git rev-parse HEAD)
push fork "https://github.com/$OSS_LOGIN/somerepo.git"
check "catches --no-verify attribution bypass at push" block $?

echo
printf 'passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
