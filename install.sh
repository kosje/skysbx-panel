#!/bin/sh
# One-line installer for the skysbx panel.
#
#   wget -qO- https://raw.githubusercontent.com/kosje/skysbx-panel/main/install.sh | sh
#
# Arguments are passed through to deploy/install-panel.sh, so a non-interactive
# install is:
#
#   wget -qO- .../install.sh | sh -s -- --domain panel.example.com --email you@example.com
#
# This file exists only to fetch the repository and hand over. Everything real
# is in deploy/install-panel.sh, which is worth reading before running either.
set -eu

REPO=${SKYSBX_REPO:-https://github.com/kosje/skysbx-panel.git}
REF=${SKYSBX_REF:-main}

RED=$(printf '\033[31m'); GRN=$(printf '\033[32m'); RST=$(printf '\033[0m')
say() { printf '%s==>%s %s\n' "$GRN" "$RST" "$*"; }
die() { printf '%s fail%s %s\n' "$RED" "$RST" "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root (sudo sh -c \"\$(wget -qO- ...)\")"

if ! command -v git >/dev/null 2>&1; then
    say "installing git"
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq && apt-get install -y -qq git
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y -q git
    elif command -v yum >/dev/null 2>&1; then
        yum install -y -q git
    else
        die "install git first"
    fi
fi

SRC=$(mktemp -d)
trap 'rm -rf "$SRC"' EXIT
say "fetching $REPO@$REF"
git clone -q --branch "$REF" --depth 1 "$REPO" "$SRC/skysbx-panel" \
    || die "cannot clone $REPO"

# A pipeline leaves stdin pointing at the downloaded script, not the terminal,
# so the installer would find nothing to prompt on and refuse. Reattach the
# terminal if there is one.
#
# The test is a subshell on purpose: on a host with no controlling terminal,
# opening /dev/tty fails, and under dash a failed redirection in the current
# shell is fatal — which used to kill this script silently, with no output at
# all, in the one situation it most needed to explain itself.
if ( exec 3>/dev/tty ) 2>/dev/null; then
    exec sh "$SRC/skysbx-panel/deploy/install-panel.sh" "$@" </dev/tty
fi
exec sh "$SRC/skysbx-panel/deploy/install-panel.sh" "$@"
