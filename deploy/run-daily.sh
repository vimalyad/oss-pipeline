#!/bin/sh
# One unattended daily cycle, as the systemd timer runs it.
#
# It runs exactly what is on main, never the working tree: this clone is also
# where changes are made, on branches, and a timer that fired mid-change would
# otherwise build and publish with half-finished code. So the first stage
# extracts main into a separate directory, links the secrets and the data
# directories into it, and re-executes this script from there. Everything
# after that -- the binary, the config, this script itself -- is main's.
#
# Everything it needs is checked before anything is attempted, and a missing
# piece fails the run loudly into the journal rather than half-running it:
# a cycle that opens pull requests under someone's name should either run
# whole or not at all.
#
#   journalctl --user -u ossp-daily        what the last runs did
#   pipeline halt "<reason>"               stop it without touching systemd
set -eu

STATE="${XDG_STATE_HOME:-$HOME/.local/state}/oss-pipeline"
LIVE="$STATE/live"

if [ "${OSSP_STAGE:-}" != live ]; then
    CLONE=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
    mkdir -p "$STATE"
    rev=$(git -C "$CLONE" rev-parse main)

    # A fresh tree per run, swapped in whole, so a run never sees a mix of
    # two revisions.
    rm -rf "$LIVE.new"
    mkdir -p "$LIVE.new"
    git -C "$CLONE" archive main | tar -x -C "$LIVE.new"

    # What main does not and must not contain: secrets, and the data the
    # pipeline accumulates. These stay in the clone, where the operator
    # looks for them.
    for f in .env config/gh-token config/identity.env config/ntfy-topic.txt config/ntfy-cmd-topic.txt; do
        [ -e "$CLONE/$f" ] && ln -s "$CLONE/$f" "$LIVE.new/$f"
    done
    for d in state reports logs work; do
        mkdir -p "$CLONE/$d"
        ln -s "$CLONE/$d" "$LIVE.new/$d"
    done

    # The binary is cached by revision, so a run on an unchanged main does
    # not rebuild.
    bin="$STATE/bin/pipeline-$rev"
    if [ ! -x "$bin" ]; then
        echo "run-daily: building the engine at main ${rev}"
        mkdir -p "$STATE/bin"
        (cd "$LIVE.new/engine" && go build -o "$bin.tmp" ./cmd/pipeline)
        mv "$bin.tmp" "$bin"
        # Keep the three newest builds.
        ls -t "$STATE"/bin/pipeline-* 2>/dev/null | tail -n +4 | xargs -r rm -f
    fi
    mkdir -p "$LIVE.new/engine/bin"
    ln -s "$bin" "$LIVE.new/engine/bin/pipeline"

    rm -rf "$LIVE"
    mv "$LIVE.new" "$LIVE"
    echo "run-daily: running main ${rev}"
    OSSP_STAGE=live exec "$LIVE/deploy/run-daily.sh"
fi

# --- from here on, everything is main's ---------------------------------------
ROOT="$LIVE"
cd "$ROOT"

# .env is the one place the database password lives; the engine's own config
# (identity, token) is read from config/ by the binary itself.
set -a
. "$ROOT/.env"
set +a
export OSSP_ROOT="$ROOT"
export DATABASE_URL="postgres://ossp:${POSTGRES_PASSWORD}@127.0.0.1:${DB_PORT:-5433}/ossp?sslmode=disable"

PIPELINE="$ROOT/engine/bin/pipeline"

# At boot the timer can fire before the network or the database is up: a
# missed 09:00 is caught up as soon as the machine is on. Wait for both rather
# than fail a run that would have worked a minute later.
#
# Giving up exits 75 (EX_TEMPFAIL), which the unit retries later; any other
# failure is not retried. A phone hotspot that is connected but passing no
# traffic is the case this exists for: the first unattended run lost its day
# to exactly that.
wait_for() {
    what=$1; shift
    i=0
    until "$@" >/dev/null 2>&1; do
        i=$((i + 1))
        if [ "$i" -gt 60 ]; then
            echo "run-daily: no $what after 30 minutes; will retry" >&2
            exit 75
        fi
        sleep 30
    done
}
wait_for "network" curl -sf --max-time 10 -o /dev/null https://api.github.com/zen
# The compose project is named after the clone's directory, not this one.
wait_for "database" docker compose -p oss-pipeline -f "$ROOT/docker-compose.yml" \
    --env-file "$ROOT/.env" exec -T db pg_isready -U ossp -d ossp

# doctor exits non-zero on anything actually wrong: identity, stored state,
# illegal history. Nothing runs past a failed doctor.
"$PIPELINE" doctor

# OSSP_DRY_RUN=1 runs the whole cycle without committing, pushing or opening
# anything -- the way to test this script in the timer's own environment:
#   systemd-run --user --wait -p Environment=OSSP_DRY_RUN=1 ... run-daily.sh
if [ "${OSSP_DRY_RUN:-}" = 1 ]; then
    exec "$PIPELINE" daily
fi
exec "$PIPELINE" daily --execute
