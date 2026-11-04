#!/usr/bin/env bash
#
# Install traccar-gateway on Ubuntu 22.04/24.04 as a systemd service.
#
#   sudo ./deploy/ubuntu/install.sh
#   sudo ./deploy/ubuntu/install.sh --domain tracker.example.com
#   sudo ./deploy/ubuntu/install.sh --dsn 'mysql://traccar:pw@127.0.0.1:3306/traccar'
#   sudo ./deploy/ubuntu/install.sh --binary ./traccar-gateway --web ./web
#
# The release zip ships this script next to the binaries and the web build, so
# running it there needs no Go toolchain:
#
#   unzip traccar-server-go_<version>.zip && cd traccar-server-go_<version>
#   sudo ./install.sh
#
# Defaults: SQLite in /opt/traccar/data, the config at
# /opt/traccar/conf/traccar.xml (the Java server's location), the gateway on
# 0.0.0.0:8082 with the traccar-web build served from /opt/traccar/web. Device
# protocol ports (5013, 5023, 5027, 8610, ...) stay at the built-in values.
# Re-run the same command to upgrade the binary and the web build; the
# configuration file is kept unless --force is passed.
set -euo pipefail

TMP_PATHS=()
cleanup() {
	((${#TMP_PATHS[@]})) || return 0
	rm -rf "${TMP_PATHS[@]}"
}
trap cleanup EXIT

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# Two layouts are supported: the repository checkout (this script in
# go_server/deploy/ubuntu, source above it) and the release zip (this script
# next to bin/linux-<arch>/traccar-gateway and web).
REPO_DIR=
for candidate in "$SCRIPT_DIR" "$SCRIPT_DIR/.." "$SCRIPT_DIR/../.."; do
	if [[ -f $candidate/go.mod && -d $candidate/cmd/traccar-gateway ]]; then
		REPO_DIR=$(cd "$candidate" && pwd)
		break
	fi
done

arch_name() {
	case $(uname -m) in
	x86_64 | amd64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	*) return 1 ;;
	esac
}

PREFIX=${PREFIX:-/opt/traccar}
# Same layout as the Java server: /opt/traccar/conf/traccar.xml.
CONF_DIR=${CONF_DIR:-}
SERVICE_USER=${SERVICE_USER:-traccar}
SERVICE_NAME=${SERVICE_NAME:-traccar-gateway}
GO_VERSION=${GO_VERSION:-1.22.12}
BINARY=${BINARY:-}
WEB_SRC=${WEB_SRC:-}
DSN=${DSN:-}
HTTP_ADDR=${HTTP_ADDR:-}
DOMAIN=${DOMAIN:-}
FORCE=${FORCE:-0}
NO_NGINX=${NO_NGINX:-0}
NEW_API_KEY=

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() {
	printf '\033[1;31merror:\033[0m %s\n' "$*" >&2
	exit 1
}

usage() {
	local cmd=./install.sh
	[[ -n $REPO_DIR ]] && cmd=./deploy/ubuntu/install.sh
	cat <<EOF
Install traccar-gateway on Ubuntu 22.04/24.04 as a systemd service.

  sudo $cmd
  sudo $cmd --domain tracker.example.com
  sudo $cmd --dsn 'mysql://traccar:password@127.0.0.1:3306/traccar'
  sudo $cmd --binary ./traccar-gateway --web ./web

In the release zip this script sits next to bin/linux-<arch>/ and web/, so it
installs the bundled binary and needs no Go toolchain.

Defaults: SQLite in /opt/traccar/data, the gateway on 0.0.0.0:8082 with the
traccar-web build served from /opt/traccar/web. Device protocol ports
(5013, 5023, 5027, 8610, ...) stay at the built-in values. Re-run the same
command to upgrade the binary and the web build; the configuration file is
kept unless --force is passed.

Options:
  --binary PATH    install a prebuilt linux binary (skips the Go toolchain)
  --web PATH       traccar-web build directory (index.html + assets)
  --dsn URL        database.url: sqlite:///path, mysql://user:pass@host/db or
                   postgres://user:pass@host/db
  --http ADDR      REST/web listen address (default 0.0.0.0:8082, or
                   127.0.0.1:8082 when --domain is used)
  --domain NAME    also install an nginx site for NAME and proxy /api to the gateway
  --prefix PATH    install prefix (default /opt/traccar)
  --conf-dir PATH  configuration directory (default <prefix>/conf)
  --user NAME      service account (default traccar)
  --force          rewrite the configuration and the API key
  --no-nginx       skip the nginx site even when --domain is set
  --help           show this help

Environment: PREFIX, CONF_DIR, SERVICE_USER, SERVICE_NAME, DSN, HTTP_ADDR,
DOMAIN, BINARY, WEB_SRC, GO_VERSION, FORCE, NO_NGINX.
EOF
}

while (($#)); do
	case $1 in
	--binary) BINARY=${2:?--binary needs a path}; shift 2 ;;
	--web) WEB_SRC=${2:?--web needs a directory}; shift 2 ;;
	--dsn) DSN=${2:?--dsn needs a url}; shift 2 ;;
	--http) HTTP_ADDR=${2:?--http needs an address}; shift 2 ;;
	--domain) DOMAIN=${2:?--domain needs a name}; shift 2 ;;
	--prefix) PREFIX=${2:?--prefix needs a path}; shift 2 ;;
	--conf-dir) CONF_DIR=${2:?--conf-dir needs a path}; shift 2 ;;
	--user) SERVICE_USER=${2:?--user needs a name}; shift 2 ;;
	--force) FORCE=1; shift ;;
	--no-nginx) NO_NGINX=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) die "unknown argument: $1 (try --help)" ;;
	esac
done

[[ -n $CONF_DIR ]] || CONF_DIR=$PREFIX/conf

DSN=${DSN:-sqlite://$PREFIX/data/traccar.db}
if [[ -z $HTTP_ADDR ]]; then
	if [[ -n $DOMAIN ]]; then HTTP_ADDR=127.0.0.1:8082; else HTTP_ADDR=0.0.0.0:8082; fi
fi
if [[ -z $WEB_SRC ]]; then
	if [[ -n $REPO_DIR && -f $REPO_DIR/web/index.html ]]; then
		WEB_SRC=$REPO_DIR/web
	elif [[ -f $SCRIPT_DIR/web/index.html ]]; then
		WEB_SRC=$SCRIPT_DIR/web
	fi
fi

[[ $EUID -eq 0 ]] || die "run as root: sudo $0"
[[ $PREFIX == /* ]] || die "--prefix must be an absolute path"
[[ $CONF_DIR == /* ]] || die "--conf-dir must be an absolute path"
[[ -n $REPO_DIR || -d $SCRIPT_DIR/bin ]] || die "no source checkout and no bin/ directory; run this script from the release zip or pass --binary"

if [[ -r /etc/os-release ]]; then
	# shellcheck disable=SC1091
	. /etc/os-release
	case ${ID:-} in
	ubuntu | debian) ;;
	*) warn "tested on Ubuntu 22.04/24.04; detected ${PRETTY_NAME:-unknown}" ;;
	esac
fi

ensure_packages() {
	local missing=()
	command -v curl >/dev/null || missing+=(curl)
	command -v openssl >/dev/null || missing+=(openssl)
	[[ -e /etc/ssl/certs/ca-certificates.crt ]] || missing+=(ca-certificates)
	((${#missing[@]})) || return 0
	log "installing ${missing[*]}"
	apt-get update -qq
	DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "${missing[@]}"
}

go_version_ok() {
	local out major minor
	out=$("$1" version 2>/dev/null) || return 1
	[[ $out =~ go([0-9]+)\.([0-9]+) ]] || return 1
	major=${BASH_REMATCH[1]}
	minor=${BASH_REMATCH[2]}
	((major > 1 || (major == 1 && minor >= 22)))
}

install_go() {
	local arch tarball tmp want got
	case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "unsupported architecture: $(uname -m); build locally with GOOS=linux GOARCH=... and use --binary" ;;
	esac
	tarball="go${GO_VERSION}.linux-${arch}.tar.gz"
	tmp=$(mktemp -d)
	TMP_PATHS+=("$tmp")
	log "downloading $tarball"
	curl -fsSL "https://go.dev/dl/$tarball" -o "$tmp/$tarball"

	if curl -fsSL "https://go.dev/dl/$tarball.sha256" -o "$tmp/sum" 2>/dev/null; then
		want=$(awk '{print $1}' "$tmp/sum")
		got=$(sha256sum "$tmp/$tarball" | awk '{print $1}')
		[[ $want == "$got" ]] || die "checksum mismatch for $tarball"
	else
		warn "no published checksum for $tarball; skipping verification"
	fi

	rm -rf /usr/local/go
	tar -C /usr/local -xzf "$tmp/$tarball"
	GO_BIN=/usr/local/go/bin/go
	log "installed $($GO_BIN version)"
}

ensure_packages

log "creating the $SERVICE_USER account and $PREFIX"
getent group "$SERVICE_USER" >/dev/null || groupadd --system "$SERVICE_USER"
id -u "$SERVICE_USER" >/dev/null 2>&1 || useradd --system \
	--gid "$SERVICE_USER" --home-dir "$PREFIX" --no-create-home \
	--shell /usr/sbin/nologin "$SERVICE_USER"

install -d -m 0755 -o root -g root "$PREFIX/bin"
install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$PREFIX/data" "$PREFIX/web"
install -d -m 0750 -o root -g "$SERVICE_USER" "$CONF_DIR"

# Older releases kept the configuration in /etc/traccar. Move it to the Java
# server's location on upgrade instead of writing a second, empty config.
LEGACY_CONF_DIR=/etc/traccar
if [[ $CONF_DIR != "$LEGACY_CONF_DIR" && ! -f $CONF_DIR/traccar.xml && -f $LEGACY_CONF_DIR/traccar.xml ]]; then
	log "moving the configuration from $LEGACY_CONF_DIR to $CONF_DIR"
	install -m 0640 -o root -g "$SERVICE_USER" \
		"$LEGACY_CONF_DIR/traccar.xml" "$CONF_DIR/traccar.xml"
	if [[ -f $LEGACY_CONF_DIR/api-key ]]; then
		install -m 0640 -o root -g "$SERVICE_USER" \
			"$LEGACY_CONF_DIR/api-key" "$CONF_DIR/api-key"
	fi
fi

BUILT_BINARY=
if [[ -n $BINARY ]]; then
	[[ -f $BINARY ]] || die "--binary $BINARY does not exist"
	BIN_SRC=$BINARY
elif [[ -n $REPO_DIR ]]; then
	GO_BIN=$(command -v go || true)
	if [[ -n $GO_BIN ]] && go_version_ok "$GO_BIN"; then
		log "using $($GO_BIN version)"
	else
		install_go
	fi
	log "building traccar-gateway"
	BUILT_BINARY=$(mktemp)
	TMP_PATHS+=("$BUILT_BINARY")
	version=$(git -C "$REPO_DIR" describe --tags --always --dirty 2>/dev/null || echo dev)
	(
		cd "$REPO_DIR"
		CGO_ENABLED=0 GOOS=linux "$GO_BIN" build -trimpath \
			-ldflags "-s -w -X main.version=$version" \
			-o "$BUILT_BINARY" ./cmd/traccar-gateway
	)
	BIN_SRC=$BUILT_BINARY
else
	arch=$(arch_name) || die "unsupported architecture $(uname -m); pass --binary"
	BIN_SRC="$SCRIPT_DIR/bin/linux-$arch/traccar-gateway"
	[[ -f $BIN_SRC ]] || die "no bundled linux-$arch binary; pass --binary"
fi
magic=$(head -c 4 "$BIN_SRC" | od -An -tx1 | tr -d ' \n')
[[ $magic == 7f454c46 ]] || die "$BIN_SRC is not a Linux ELF binary (build it with GOOS=linux GOARCH=amd64)"
log "installing $BIN_SRC"
install -m 0755 -o root -g root "$BIN_SRC" "$PREFIX/bin/traccar-gateway"

if [[ -n $WEB_SRC && $WEB_SRC != "$PREFIX/web" ]]; then
	[[ -f $WEB_SRC/index.html ]] || die "--web $WEB_SRC has no index.html; point it at a traccar-web build"
	log "installing the web build from $WEB_SRC"
	cp -a "$WEB_SRC/." "$PREFIX/web/"
	chown -R "$SERVICE_USER:$SERVICE_USER" "$PREFIX/web"
elif [[ ! -f $PREFIX/web/index.html ]]; then
	warn "no web build installed; the REST API works but / returns 404"
fi

xml_escape() { sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'; }

if [[ -f $CONF_DIR/traccar.xml && $FORCE -eq 0 ]]; then
	log "keeping $CONF_DIR/traccar.xml (use --force to rewrite it)"
else
	NEW_API_KEY=$(openssl rand -hex 24)
	(
		umask 027
		cat >"$CONF_DIR/traccar.xml" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE properties SYSTEM "http://java.sun.com/dtd/properties.dtd">
<properties>
    <!-- Written by install.sh; flags and TRACCAR_* override it. -->
    <entry key='web.address'>$(printf '%s' "$HTTP_ADDR" | xml_escape)</entry>
    <entry key='database.url'>$(printf '%s' "$DSN" | xml_escape)</entry>
    <entry key='web.path'>$(printf '%s' "$PREFIX/web" | xml_escape)</entry>
    <entry key='media.path'>$(printf '%s' "$PREFIX/data/media" | xml_escape)</entry>
    <entry key='api.key'>$NEW_API_KEY</entry>
</properties>
EOF
		printf '%s\n' "$NEW_API_KEY" >"$CONF_DIR/api-key"
	)
	chown root:"$SERVICE_USER" "$CONF_DIR/traccar.xml" "$CONF_DIR/api-key"
	chmod 0640 "$CONF_DIR/traccar.xml" "$CONF_DIR/api-key"
	log "wrote $CONF_DIR/traccar.xml"
fi

log "installing the systemd unit"
sed -e "s|@PREFIX@|$PREFIX|g" \
	-e "s|@CONF_DIR@|$CONF_DIR|g" \
	-e "s|@SERVICE_USER@|$SERVICE_USER|g" \
	"$SCRIPT_DIR/traccar-gateway.service.in" >"/etc/systemd/system/$SERVICE_NAME.service"

systemctl daemon-reload
systemctl enable "$SERVICE_NAME" >/dev/null
systemctl restart "$SERVICE_NAME"

health_addr=${HTTP_ADDR/0.0.0.0/127.0.0.1}
if [[ $health_addr == :* ]]; then health_addr=127.0.0.1$health_addr; fi
health_url="http://$health_addr/api/health"

log "waiting for $health_url"
healthy=0
for _ in $(seq 1 30); do
	# is-active keeps a stale listener on the same port from faking success.
	if systemctl is-active --quiet "$SERVICE_NAME" &&
		curl -fsS --max-time 2 "$health_url" >/dev/null 2>&1; then
		healthy=1
		break
	fi
	sleep 1
done
if ((!healthy)); then
	systemctl status --no-pager -l "$SERVICE_NAME" || true
	journalctl -u "$SERVICE_NAME" -n 30 --no-pager || true
	die "$SERVICE_NAME did not become healthy; see the log above"
fi

if [[ -n $DOMAIN && $NO_NGINX -eq 0 ]]; then
	if ! command -v nginx >/dev/null; then
		log "installing nginx"
		apt-get update -qq
		DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends nginx
	fi
	sed -e "s|@DOMAIN@|$DOMAIN|g" \
		"$SCRIPT_DIR/nginx.conf.in" >/etc/nginx/sites-available/traccar-gateway
	ln -sf /etc/nginx/sites-available/traccar-gateway /etc/nginx/sites-enabled/traccar-gateway
	rm -f /etc/nginx/sites-enabled/default
	nginx -t
	systemctl enable nginx >/dev/null 2>&1 || true
	systemctl reload nginx 2>/dev/null || systemctl restart nginx
	log "nginx site installed for $DOMAIN"
fi

cat <<EOF

traccar-gateway is running.

  service   systemctl status $SERVICE_NAME
  logs      journalctl -u $SERVICE_NAME -f
  config    $CONF_DIR/traccar.xml
  data      $PREFIX/data
  web       $PREFIX/web
EOF

if [[ -n $NEW_API_KEY ]]; then
	cat <<EOF
  API key   $NEW_API_KEY
            (also in $CONF_DIR/api-key)
EOF
else
	cat <<EOF
  API key   $CONF_DIR/api-key
EOF
fi

cat <<EOF

  Open the web interface and register the first account; it becomes the
  administrator. Afterwards anonymous registration is closed again.
EOF

if [[ -n $DOMAIN ]]; then
	cat <<EOF

  UI        http://$DOMAIN/   (after DNS points here)
  TLS       apt-get install -y certbot python3-certbot-nginx && certbot --nginx -d $DOMAIN
EOF
else
	cat <<EOF

  UI        http://<server-ip>:${HTTP_ADDR##*:}/
EOF
	case $HTTP_ADDR in
	0.0.0.0:* | \[::\]:*)
		warn "the web UI and REST API are on plain HTTP; re-run with --domain to add nginx + TLS"
		;;
	127.0.0.1:* | \[::1\]:*)
		warn "the gateway only listens on loopback; put nginx in front or pass --http 0.0.0.0:${HTTP_ADDR##*:}"
		;;
	esac
fi

cat <<EOF

Device ports (open these in ufw and in the provider's firewall/security group):
  H02 5013/tcp  GT06 5023/tcp  Teltonika 5027/tcp  JT808 8610/tcp
  OsmAnd 5055/tcp  MQTT 1883/tcp
  GPS103 5002/tcp  TK103 5001/tcp  Meitrack 5020/tcp  Wondex 5006/tcp
  Suntech 5011/tcp  Ruptela 5046/tcp
EOF
