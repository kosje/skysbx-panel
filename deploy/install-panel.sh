#!/usr/bin/env bash
# Install the skysb panel on a Debian/Ubuntu host.
#
#   sudo ./install-panel.sh --domain panel.example.com --email you@example.com
#
# Re-running upgrades the binary in place; the database is never touched.
set -euo pipefail

ROOT=${SKYSB_ROOT:-/opt/skysb}
DOMAIN=""
EMAIL=""
SRC_DIR=""
GH_TOKEN=${GITHUB_TOKEN:-}
GH_OWNER=${SKYSB_GH_OWNER:-kosje}
REF=${SKYSB_REF:-main}

RED=$'\e[31m'; GRN=$'\e[32m'; YLW=$'\e[33m'; BLD=$'\e[1m'; RST=$'\e[0m'
say()  { printf '%s==>%s %s\n' "$BLD" "$RST" "$*"; }
ok()   { printf '%s  ok%s %s\n' "$GRN" "$RST" "$*"; }
warn() { printf '%s warn%s %s\n' "$YLW" "$RST" "$*"; }
die()  { printf '%s fail%s %s\n' "$RED" "$RST" "$*" >&2; exit 1; }

usage() {
    cat <<EOF
Usage: sudo ./install-panel.sh --domain <fqdn> [options]

  --domain <fqdn>   Panel domain. Must already resolve to this server.
  --email <addr>    Contact address for Let's Encrypt (recommended).
  --src <dir>       Build from a checkout already on disk instead of cloning.
  -h, --help        This text.

Ports 80 and 443 must be free: the panel terminates its own TLS and answers the
ACME challenge itself, so there is no reverse proxy to install or configure.
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --domain) DOMAIN=$2; shift 2 ;;
        --email)  EMAIL=$2; shift 2 ;;
        --src)    SRC_DIR=$2; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown option: $1 (try --help)" ;;
    esac
done

# ─────────────────────────────── preflight ────────────────────────────────

say "preflight"
[ "$(id -u)" = 0 ] || die "run as root"

if [ -z "$DOMAIN" ]; then
    if [ -t 0 ]; then
        printf '  Panel domain (must already resolve here): '
        read -r DOMAIN
    fi
    [ -n "$DOMAIN" ] || { usage; die "--domain is required"; }
fi
if [ -z "$EMAIL" ] && [ -t 0 ]; then
    printf "  Let's Encrypt contact email [skip]: "
    read -r EMAIL
fi

command -v curl >/dev/null || { apt-get update -qq && apt-get install -y -qq curl; }
for p in git dig; do
    command -v "$p" >/dev/null || apt-get install -y -qq git dnsutils
done

PUBLIC_IP=$(curl -fsS --max-time 10 https://api.ipify.org || echo "")
RESOLVED=$(dig +short "$DOMAIN" A @1.1.1.1 | tail -1)
if [ -z "$RESOLVED" ]; then
    die "$DOMAIN has no A record"
elif [ -n "$PUBLIC_IP" ] && [ "$RESOLVED" != "$PUBLIC_IP" ]; then
    warn "$DOMAIN resolves to $RESOLVED but this host is $PUBLIC_IP"
    warn "the ACME HTTP-01 challenge needs a direct route, so turn off any proxy"
    if [ -t 0 ]; then
        printf '  continue anyway? [y/N] '
        read -r a; [ "$a" = y ] || [ "$a" = Y ] || exit 1
    fi
else
    ok "$DOMAIN -> $RESOLVED (this host)"
fi

# Only check the ports on a first install: on an upgrade they are held by the
# panel this script is about to replace.
if ! systemctl is-enabled --quiet skysb-panel 2>/dev/null; then
    for port in 80 443; do
        if ss -tlnH | awk '{print $4}' | grep -qE "[:.]$port\$"; then
            die "port $port is in use; the panel terminates its own TLS and needs both"
        fi
    done
    ok "ports 80 and 443 are free"
fi

# ──────────────────────────────── build ───────────────────────────────────

install -d -m 0700 "$ROOT"
BUILD=$ROOT/build
mkdir -p "$BUILD"

say "sources"
if [ -n "$SRC_DIR" ]; then
    rm -rf "$BUILD/skysb-panel"
    cp -a "$SRC_DIR" "$BUILD/skysb-panel"
    ok "using $SRC_DIR"
else
    URL="https://github.com/${GH_OWNER}/skysb-panel.git"
    [ -n "$GH_TOKEN" ] && URL="https://${GH_TOKEN}@github.com/${GH_OWNER}/skysb-panel.git"
    rm -rf "$BUILD/skysb-panel"
    git clone -q --branch "$REF" --depth 1 "$URL" "$BUILD/skysb-panel" \
        || die "cannot clone ${GH_OWNER}/skysb-panel (a private repo needs GITHUB_TOKEN)"
    ok "$(git -C "$BUILD/skysb-panel" rev-parse --short HEAD)"
fi

# Sources that travelled through a Windows checkout carry CRLF, and bash then
# fails on "bad interpreter".
find "$BUILD" -type f -name '*.sh' -exec sed -i 's/\r$//' {} + 2>/dev/null || true

if ! command -v docker >/dev/null; then
    say "installing docker (used only to build; nothing runs in it)"
    curl -fsSL https://get.docker.com | sh >/dev/null
fi

say "building"
docker run --rm -v "$BUILD/skysb-panel:/src" -w /src \
    -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=0 -e GOOS=linux \
    golang:1.27 \
    go build -trimpath -ldflags '-s -w' -o /src/skysb-panel ./cmd/panel
install -m 0755 "$BUILD/skysb-panel/skysb-panel" "$ROOT/skysb-panel"
ok "panel binary installed"

# ─────────────────────────────── service ──────────────────────────────────

say "service"
cat > /etc/systemd/system/skysb-panel.service <<EOF
[Unit]
Description=skysb panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${ROOT}
ExecStart=${ROOT}/skysb-panel --domain ${DOMAIN} --acme-email ${EMAIL} --db ${ROOT}/skysb.db
Restart=always
RestartSec=3

# Binding 80 and 443 is the only privilege it needs.
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=${ROOT}

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable -q skysb-panel
systemctl restart skysb-panel
ok "systemd unit installed"

printf '    waiting for a certificate '
for _ in $(seq 1 60); do
    if curl -fsS --max-time 5 "https://$DOMAIN/login" >/dev/null 2>&1; then
        printf '\n'; ok "https://$DOMAIN is live"
        break
    fi
    printf '.'; sleep 3
done

cat <<EOF

${GRN}skysb panel
===========
Panel     https://${DOMAIN}
Setup     https://${DOMAIN}/setup   ← create the administrator here
Data      ${ROOT}/skysb.db          ← the whole of the panel's state

Logs      journalctl -u skysb-panel -f

Next: open the setup page, then add a node and copy its join token.${RST}
EOF
