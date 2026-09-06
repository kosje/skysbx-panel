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
ACTION=install

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
  --version             Show the installed panel and node versions.
  --upgrade             Rebuild and restart both services.
  --uninstall           Remove both services; keep their data and certificates.
  --purge               Remove both services and all data (cannot be undone).
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
        --version) ACTION=version; shift ;;
        --upgrade) ACTION=upgrade; shift ;;
        --uninstall) ACTION=uninstall; shift ;;
        --purge) ACTION=purge; shift ;;
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
if [ "$ACTION" = install ] && [ -z "$DOMAIN" ] && [ -t 0 ]; then
    printf '  Panel domain (must already resolve here): '
    read -r DOMAIN
fi
[ "$ACTION" != install ] || [ -n "$DOMAIN" ] \
    || { usage >&2; die '--domain is required without a terminal'; }

if [ "$ACTION" = install ] && [ -z "$EMAIL" ] && [ -t 0 ]; then
    printf "  Let's Encrypt contact email [skip]: "
    read -r EMAIL
fi
[ "$ACTION" != install ] || PANEL_URL=${PANEL_URL:-"https://$DOMAIN"}

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
if [ "$ACTION" = install ]; then
    set -- --domain "$DOMAIN"
    [ -n "$EMAIL" ] && set -- "$@" --email "$EMAIL"
else
    set -- "--$ACTION"
fi
bash "$SRC/skysbx-panel/deploy/install-panel.sh" "$@"

if [ "$ACTION" = install ] || [ "$ACTION" = upgrade ]; then
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
fi

if [ "$ACTION" = install ] && [ -z "$TOKEN" ]; then
    if [ -t 0 ]; then
        printf '\nCreate a node at %s, then paste its one-time join token: ' "$PANEL_URL"
        read -r TOKEN
    fi
    [ -n "$TOKEN" ] || die 'a node join token is required; rerun with --token <token>'
fi

say "fetching $NODE_REPO@$NODE_REF installer"
git clone -q --branch "$NODE_REF" --depth 1 "$NODE_REPO" "$SRC/skysbx-node" \
    || die "cannot clone $NODE_REPO"
# Call the real installer directly instead of the node repository's launcher.
# Its launcher deliberately reattaches /dev/tty for standalone interactive
# use. All required node values are already supplied here, and leaving that
# tty attached can keep this combined command alive after it prints success.
NODE_INSTALL=$SRC/skysbx-node/deploy/install-node.sh
[ -f "$NODE_INSTALL" ] || die "node installer is missing from $NODE_REPO"

if [ "$ACTION" = install ]; then
    set -- --panel "$PANEL_URL" --token "$TOKEN" --src "$SRC/skysbx-node"
elif [ "$ACTION" = upgrade ]; then
    set -- "--$ACTION" --src "$SRC/skysbx-node"
else
    set -- "--$ACTION"
fi

# install-panel.sh leaves these Docker-mounted caches under $ROOT. The node is
# a separate repository, so its installer cannot otherwise see the panel
# build's Go module cache. Put a tiny docker shim first in PATH only while the
# node installer runs; every `docker run` it makes mounts the same caches.
# System packages and the Docker daemon are already installed by the panel
# installer, while shared Go modules now come from the local cache as well.
if [ "$ACTION" = install ] || [ "$ACTION" = upgrade ]; then
    REAL_DOCKER=$(command -v docker)
    [ -n "$REAL_DOCKER" ] || die "docker disappeared after installing the panel"
    GO_MOD_CACHE=$ROOT/go-mod-cache
    GO_BUILD_CACHE=$ROOT/go-build-cache
    install -d -m 0700 "$GO_MOD_CACHE" "$GO_BUILD_CACHE"
    DOCKER_SHIM_DIR=$SRC/docker-shim
    mkdir -p "$DOCKER_SHIM_DIR"
    cat > "$DOCKER_SHIM_DIR/docker" <<'EOF'
#!/bin/sh
set -eu
if [ "${1-}" = run ]; then
    shift
    exec "$SKYSBX_REAL_DOCKER" run \
        -v "$SKYSBX_GO_MOD_CACHE:/go/pkg/mod" \
        -v "$SKYSBX_GO_BUILD_CACHE:/root/.cache/go-build" "$@"
fi
exec "$SKYSBX_REAL_DOCKER" "$@"
EOF
    chmod 700 "$DOCKER_SHIM_DIR/docker"
fi

# The two repositories intentionally use the same SKYSBX_REPO variable for
# their standalone launchers.  Set it explicitly here so a custom panel source
# cannot accidentally be cloned as the node source.
if [ "$ACTION" = install ] || [ "$ACTION" = upgrade ]; then
    PATH="$DOCKER_SHIM_DIR:$PATH" \
    SKYSBX_REAL_DOCKER=$REAL_DOCKER \
    SKYSBX_GO_MOD_CACHE=$GO_MOD_CACHE \
    SKYSBX_GO_BUILD_CACHE=$GO_BUILD_CACHE \
    SKYSBX_REPO=$NODE_REPO SKYSBX_REF=$NODE_REF bash "$NODE_INSTALL" "$@" </dev/null
else
    SKYSBX_REPO=$NODE_REPO SKYSBX_REF=$NODE_REF bash "$NODE_INSTALL" "$@" </dev/null
fi

printf '\n%sskysbx panel and node are installed on this host.%s\n' "$GRN" "$RST"
