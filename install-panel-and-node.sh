#!/bin/sh
# Install skysbx-panel and skysbx-node on this host.
#
#   wget -qO- https://raw.githubusercontent.com/kosje/skysbx-panel/main/install-panel-and-node.sh | sh
#
# The node must have a join token.  Create a node in the panel after the panel
# installer finishes, then paste that one-time token when this script asks.
set -eu

PANEL_REPO=${SKYSBX_REPO:-https://github.com/kosje/skysbx-panel.git}
PANEL_REF=${SKYSBX_REF:-main}
NODE_REPO=${SKYSBX_NODE_REPO:-https://github.com/kosje/skysbx-node.git}
NODE_REF=${SKYSBX_NODE_REF:-main}
ROOT=${SKYSBX_ROOT:-/opt/skysbx}
DOMAIN=""
EMAIL=""
PANEL_URL=""
TOKEN=""

RED=$(printf '\033[31m'); GRN=$(printf '\033[32m'); BLD=$(printf '\033[1m'); RST=$(printf '\033[0m')
say() { printf '%s==>%s %s\n' "$BLD" "$RST" "$*"; }
ok() { printf '%s  ok%s %s\n' "$GRN" "$RST" "$*"; }
die() { printf '%s fail%s %s\n' "$RED" "$RST" "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
Usage: sudo sh install-panel-and-node.sh [options]

Installs the panel first, then installs a node connected to that same panel.
After the panel is online, create a node in its web UI and paste the one-time
join token when prompted (or provide it with --token).

  --domain <fqdn>       Panel domain (prompts when omitted; must resolve here).
  --email <addr>        Let's Encrypt contact email for the panel.
  --panel <url>         Panel URL for the node (default: https://<panel-fqdn>).
  --token <token>       Node join token created in the panel UI.
  -h, --help            Show this help.

The node reuses the certificate already obtained by the local panel. Do not
pass a node domain: requesting another HTTP-01 certificate would contend with
the panel for port 80.

Environment overrides: SKYSBX_REPO, SKYSBX_REF (panel), SKYSBX_NODE_REPO,
SKYSBX_NODE_REF (node), GITHUB_TOKEN.
EOF
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --domain) DOMAIN=${2-}; shift 2 ;;
        --email) EMAIL=${2-}; shift 2 ;;
        --panel) PANEL_URL=${2-}; shift 2 ;;
        --token) TOKEN=${2-}; shift 2 ;;
        --node-domain|--cf-token)
            die "$1 is not supported for a same-host install; the node reuses the panel certificate" ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown option: $1 (try --help)" ;;
    esac
done

[ "$(id -u)" = 0 ] || die 'run as root (for example: sudo sh -c "$(wget -qO- ...) ")'

# Like the single-component launchers, recover the terminal after wget | sh so
# the panel administrator and node-token prompts remain interactive.
if (exec 3>/dev/tty) 2>/dev/null; then
    exec </dev/tty
fi

# Keep the usual installation path fully interactive. Command-line options are
# still useful for automation, but a person should not have to remember a set
# of environment variables or flags just to begin a same-host installation.
if [ -z "$DOMAIN" ] && [ -t 0 ]; then
    printf '  Panel domain (must already resolve here): '
    read -r DOMAIN
fi
[ -n "$DOMAIN" ] || { usage >&2; die '--domain is required without a terminal'; }

if [ -z "$EMAIL" ] && [ -t 0 ]; then
    printf "  Let's Encrypt contact email [skip]: "
    read -r EMAIL
fi
PANEL_URL=${PANEL_URL:-"https://$DOMAIN"}

if ! command -v git >/dev/null 2>&1; then
    say 'installing git'
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq && apt-get install -y -qq git
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y -q git
    elif command -v yum >/dev/null 2>&1; then
        yum install -y -q git
    else
        die 'install git first'
    fi
fi
command -v bash >/dev/null 2>&1 || die 'bash is required'

SRC=$(mktemp -d)
trap 'rm -rf "$SRC"' EXIT
say "fetching $PANEL_REPO@$PANEL_REF"
git clone -q --branch "$PANEL_REF" --depth 1 "$PANEL_REPO" "$SRC/skysbx-panel" \
    || die "cannot clone $PANEL_REPO"

# Run the real panel installer from the checked-out source, rather than piping
# it, so its administrator prompt keeps a usable stdin.
set -- --domain "$DOMAIN"
[ -n "$EMAIL" ] && set -- "$@" --email "$EMAIL"
bash "$SRC/skysbx-panel/deploy/install-panel.sh" "$@"

# CertMagic keeps the panel certificate under its own storage tree, while the
# node's default AnyTLS paths are /opt/skysbx/cert.pem and key.pem. Symlinking
# them lets both services use one certificate and prevents the node installer
# from starting certbot, which would otherwise fail because the panel owns :80.
PANEL_CERT=$(find "$ROOT/certs/certificates" -type f -name "$DOMAIN.crt" -print -quit 2>/dev/null || true)
PANEL_KEY=${PANEL_CERT%.crt}.key
[ -n "$PANEL_CERT" ] && [ -f "$PANEL_KEY" ] \
    || die "cannot find the panel certificate for $DOMAIN under $ROOT/certs"
ln -sfn "$PANEL_CERT" "$ROOT/cert.pem"
ln -sfn "$PANEL_KEY" "$ROOT/key.pem"
ok "node will reuse the panel certificate"

if [ -z "$TOKEN" ]; then
    if [ -t 0 ]; then
        printf '\nCreate a node at %s, then paste its one-time join token: ' "$PANEL_URL"
        read -r TOKEN
    fi
    [ -n "$TOKEN" ] || die 'a node join token is required; rerun with --token <token>'
fi

say "fetching $NODE_REPO@$NODE_REF installer"
git clone -q --branch "$NODE_REF" --depth 1 "$NODE_REPO" "$SRC/skysbx-node" \
    || die "cannot clone $NODE_REPO"
NODE_INSTALL=$SRC/skysbx-node/install.sh
[ -f "$NODE_INSTALL" ] || die "node installer is missing from $NODE_REPO"

set -- --panel "$PANEL_URL" --token "$TOKEN"

# The two repositories intentionally use the same SKYSBX_REPO variable for
# their standalone launchers.  Set it explicitly here so a custom panel source
# cannot accidentally be cloned as the node source.
SKYSBX_REPO=$NODE_REPO SKYSBX_REF=$NODE_REF sh "$NODE_INSTALL" "$@"

printf '\n%sskysbx panel and node are installed on this host.%s\n' "$GRN" "$RST"
