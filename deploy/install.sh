#!/bin/sh
# Installs the daily timer for the current user and makes it run without a
# login: lingering starts this user's systemd instance at boot.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
mkdir -p "$UNIT_DIR"
ln -sf "$ROOT/deploy/systemd/ossp-daily.service" "$UNIT_DIR/ossp-daily.service"
ln -sf "$ROOT/deploy/systemd/ossp-daily.timer" "$UNIT_DIR/ossp-daily.timer"
systemctl --user daemon-reload
systemctl --user enable --now ossp-daily.timer
loginctl enable-linger "$(id -un)"
systemctl --user list-timers ossp-daily.timer --no-pager
