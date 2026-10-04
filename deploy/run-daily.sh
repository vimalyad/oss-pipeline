#!/bin/sh
# One unattended daily cycle, as the systemd timer runs it.
#
# Everything it needs is checked before anything is attempted, and a missing
# piece fails the run loudly into the journal rather than half-running it:
# a cycle that opens pull requests under someone's name should either run
# whole or not at all.
#
#   journalctl --user -u ossp-daily        what the last runs did
#   pipeline halt "<reason>"               stop it without touching systemd
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
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
wait_for "the network" curl -sf --max-time 10 -o /dev/null https://api.github.com/zen
wait_for "the database" docker compose -f "$ROOT/docker-compose.yml" exec -T db pg_isready -U ossp -d ossp

# Rebuild if the source moved on, so the timer never runs a stale binary
# after a pull.
if [ ! -x "$PIPELINE" ] || [ -n "$(find "$ROOT/engine" -name '*.go' -newer "$PIPELINE" -print -quit)" ]; then
    echo "run-daily: building the engine"
    (cd "$ROOT/engine" && go build -o "$PIPELINE" ./cmd/pipeline)
fi

# doctor exits non-zero on anything actually wrong: identity, stored state,
# illegal history. Nothing runs past a failed doctor.
"$PIPELINE" doctor

# OSSP_DRY_RUN=1 runs the whole cycle without committing, pushing or opening
# anything -- the way to test this script in the timer's own environment:
#   systemd-run --user --wait -p Environment=OSSP_DRY_RUN=1 -p WorkingDirectory=... run-daily.sh
if [ "${OSSP_DRY_RUN:-}" = 1 ]; then
    exec "$PIPELINE" daily
fi
exec "$PIPELINE" daily --execute
