#!/usr/bin/env bash
# Install the skysbx panel on a Debian/Ubuntu host.
#
#   sudo ./install-panel.sh --domain panel.example.com --email you@example.com
#
# Re-running upgrades the binary in place; the database is never touched.
set -euo pipefail

ROOT=${SKYSBX_ROOT:-/opt/skysbx}
DOMAIN=""
EMAIL=""
SRC_DIR=""
GH_TOKEN=${GITHUB_TOKEN:-}
GH_OWNER=${SKYSBX_GH_OWNER:-kosje}
REF=${SKYSBX_REF:-main}

RED=$'\e[31m'; GRN=$'\e[32m'; YLW=$'\e[33m'; BLD=$'\e[1m'; RST=$'\e[0m'
say()  { printf '%s==>%s %s\n' "$BLD" "$RST" "$*"; }
ok()   { printf '%s  ok%s %s\n' "$GRN" "$RST" "$*"; }
warn() { printf '%s warn%s %s\n' "$YLW" "$RST" "$*"; }
die()  { printf '%s fail%s %s\n' "$RED" "$RST" "$*" >&2; exit 1; }

ACTION=install

usage() {
    cat <<EOF
Usage: sudo ./install-panel.sh [--domain <fqdn>] [options]

Actions (default: install)
  --version         What is installed.
  --upgrade         Rebuild from the current sources and restart. Reads the
                    domain back from the systemd unit, so it needs no
                    arguments. The database is never touched.
  --uninstall       Stop and remove the service and the binary. Keeps the
                    database, the certificates and the domain, so putting it
                    back is a no-argument --upgrade.
  --purge           --uninstall, and delete the database and certificates too.
                    That is every user, node and subscription — there is no
                    undo, and no copy anywhere else.

Install options
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
        --version)   ACTION=version; shift ;;
        --upgrade)   ACTION=upgrade; shift ;;
        --uninstall) ACTION=uninstall; shift ;;
        --purge)     ACTION=purge; shift ;;
        --domain)    DOMAIN=$2; shift 2 ;;
        --email)     EMAIL=$2; shift 2 ;;
        --src)       SRC_DIR=$2; shift 2 ;;
        -h|--help)   usage; exit 0 ;;
        *) die "unknown option: $1 (try --help)" ;;
    esac
done

# ───────────────────────── version / uninstall / purge ─────────────────────

if [ "$ACTION" = version ]; then
    if [ -x "$ROOT/skysbx-panel" ]; then
        "$ROOT/skysbx-panel" -version
        printf 'installed  %s\n' "$(stat -c %y "$ROOT/skysbx-panel" 2>/dev/null | cut -d. -f1)"
        systemctl is-active --quiet skysbx-panel \
            && printf 'service    running\n' || printf 'service    not running\n'
        [ -f "$ROOT/skysbx.db" ] && printf 'database   %s (%s)\n' "$ROOT/skysbx.db" \
            "$(du -h "$ROOT/skysbx.db" 2>/dev/null | cut -f1)"
    else
        printf 'skysbx-panel is not installed at %s\n' "$ROOT"
    fi
    exit 0
fi

if [ "$ACTION" = uninstall ] || [ "$ACTION" = purge ]; then
    [ "$(id -u)" = 0 ] || die "run as root"

    say "removing the service"
    systemctl disable --now skysbx-panel >/dev/null 2>&1 || true
    rm -f /etc/systemd/system/skysbx-panel.service
    systemctl daemon-reload 2>/dev/null || true
    systemctl reset-failed 2>/dev/null || true
    ok "skysbx-panel stopped and removed"

    rm -f "$ROOT/skysbx-panel"
    # The build tree is scratch space, not data: a fresh clone on every run.
    rm -rf "$ROOT/build/skysbx-panel"
    ok "binary and build cache removed"

    if [ "$ACTION" = purge ]; then
        say "purging"
        # Every user, node and subscription lives in this one file. Nothing else
        # in this script destroys anything that cannot be rebuilt.
        rm -f "$ROOT/skysbx.db" "$ROOT/skysbx.db-wal" "$ROOT/skysbx.db-shm"
        rm -f "$ROOT/panel.env"
        rm -rf "$ROOT/certs"
        ok "database and certificates deleted"
        if command -v docker >/dev/null 2>&1; then
            docker image rm golang:1.27 >/dev/null 2>&1 \
                && ok "build image removed" || true
        fi
        if [ -f "$ROOT/.docker-installed-by-skysbx" ] && command -v docker >/dev/null 2>&1; then
            say "removing docker (this script installed it)"
            systemctl disable --now docker docker.socket containerd >/dev/null 2>&1 || true
            apt-get purge -y -qq docker-ce docker-ce-cli containerd.io \
                docker-buildx-plugin docker-compose-plugin >/dev/null 2>&1 || true
            apt-get autoremove -y -qq >/dev/null 2>&1 || true
            rm -rf /var/lib/docker /var/lib/containerd /etc/docker
            rm -f "$ROOT/.docker-installed-by-skysbx"
            ok "docker removed"
        fi
    fi

    # Shared with the node when both are on one host, so it goes only if this
    # was the last thing in it.
    rmdir "$ROOT/build" 2>/dev/null || true
    if rmdir "$ROOT" 2>/dev/null; then
        ok "$ROOT removed"
    else
        warn "$ROOT kept — it still holds files (the node's, or your own):"
        ls -A "$ROOT" 2>/dev/null | sed 's/^/       /'
    fi

    printf '\n%sskysbx panel removed.%s\n' "$GRN" "$RST"
    [ "$ACTION" = uninstall ] && printf \
        'The database and certificates were kept at %s; --purge removes those too.\n' "$ROOT"
    exit 0
fi

if [ "$ACTION" = upgrade ]; then
    # panel.env first: it survives an --uninstall, so "reinstall" and "upgrade"
    # are the same command. The unit file is the fallback for panels installed
    # before panel.env existed.
    if [ -f "$ROOT/panel.env" ]; then
        DOMAIN=${DOMAIN:-$(sed -n 's/^SKYSBX_DOMAIN=//p' "$ROOT/panel.env")}
        EMAIL=${EMAIL:-$(sed -n 's/^SKYSBX_ACME_EMAIL=//p' "$ROOT/panel.env")}
    elif [ -f /etc/systemd/system/skysbx-panel.service ]; then
        DOMAIN=${DOMAIN:-$(sed -n 's/.*--domain \([^ ]*\).*/\1/p' \
            /etc/systemd/system/skysbx-panel.service | head -1)}
        EMAIL=${EMAIL:-$(sed -n 's/.*--acme-email \([^ ]*\).*/\1/p' \
            /etc/systemd/system/skysbx-panel.service | head -1)}
    fi
    [ -n "$DOMAIN" ] || die "cannot tell which domain this panel serves; pass --domain"
    say "upgrading — domain $DOMAIN"
fi

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
if ! systemctl is-enabled --quiet skysbx-panel 2>/dev/null; then
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
    rm -rf "$BUILD/skysbx-panel"
    cp -a "$SRC_DIR" "$BUILD/skysbx-panel"
    ok "using $SRC_DIR"
else
    URL="https://github.com/${GH_OWNER}/skysbx-panel.git"
    rm -rf "$BUILD/skysbx-panel"
    # The token goes in a per-command header, not in the URL: git writes the
    # remote URL into the clone's .git/config, and a token in it would sit on
    # disk for as long as the build directory does.
    if [ -n "$GH_TOKEN" ]; then
        git -c "http.extraHeader=Authorization: Basic $(printf 'x-access-token:%s' \
            "$GH_TOKEN" | base64 -w0)" \
            clone -q --branch "$REF" --depth 1 "$URL" "$BUILD/skysbx-panel" \
            || die "cannot clone ${GH_OWNER}/skysbx-panel (check GITHUB_TOKEN)"
    else
        git clone -q --branch "$REF" --depth 1 "$URL" "$BUILD/skysbx-panel" \
            || die "cannot clone ${GH_OWNER}/skysbx-panel (a private repo needs GITHUB_TOKEN)"
    fi
    ok "$(git -C "$BUILD/skysbx-panel" rev-parse --short HEAD)"
fi

# Sources that travelled through a Windows checkout carry CRLF, and bash then
# fails on "bad interpreter".
find "$BUILD" -type f -name '*.sh' -exec sed -i 's/\r$//' {} + 2>/dev/null || true

if ! command -v docker >/dev/null; then
    say "installing docker (used only to build; nothing runs in it)"
    curl -fsSL https://get.docker.com | sh >/dev/null
    # Remembered so --purge can remove Docker again without guessing. A host
    # that already had it is running something in it.
    touch "$ROOT/.docker-installed-by-skysbx"
fi

# Stamped into the binary so `--version` can answer what is running without
# anyone reading a build log.
VER=$(git -C "$BUILD/skysbx-panel" rev-parse --short HEAD 2>/dev/null || echo unknown)

say "building"
docker run --rm -v "$BUILD/skysbx-panel:/src" -w /src \
    -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=0 -e GOOS=linux \
    golang:1.27 \
    go build -trimpath -ldflags "-s -w -X main.version=$VER" -o /src/skysbx-panel ./cmd/panel
install -m 0755 "$BUILD/skysbx-panel/skysbx-panel" "$ROOT/skysbx-panel"
ok "panel binary installed"

# ─────────────────────────────── service ──────────────────────────────────

say "service"
# Kept beside the data rather than only in the unit file, so that --upgrade
# still knows the domain after an --uninstall has removed the unit. Without it,
# reinstalling would mean remembering and retyping what the panel already knew.
cat > "$ROOT/panel.env" <<EOF
SKYSBX_DOMAIN=${DOMAIN}
SKYSBX_ACME_EMAIL=${EMAIL}
EOF
chmod 600 "$ROOT/panel.env"

cat > /etc/systemd/system/skysbx-panel.service <<EOF
[Unit]
Description=skysbx panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${ROOT}
ExecStart=${ROOT}/skysbx-panel --domain ${DOMAIN} --acme-email ${EMAIL} --db ${ROOT}/skysbx.db
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
systemctl enable -q skysbx-panel
systemctl restart skysbx-panel
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

${GRN}skysbx panel
===========
Panel     https://${DOMAIN}
Setup     https://${DOMAIN}/setup   ← create the administrator here
Data      ${ROOT}/skysbx.db          ← the whole of the panel's state

Logs      journalctl -u skysbx-panel -f

Next: open the setup page, then add a node and copy its join token.${RST}
EOF
