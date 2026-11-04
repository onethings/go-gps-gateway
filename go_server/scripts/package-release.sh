#!/usr/bin/env bash
#
# Build the self-contained Linux release zip: the gateway for one or both
# architectures, the traccar-web build and the Ubuntu installer. The result
# needs no Go toolchain and no checkout on the target machine:
#
#   unzip traccar-server-go_<version>.zip
#   cd traccar-server-go_<version>
#   sudo ./install.sh
#
#   ./scripts/package-release.sh                  # amd64 and arm64
#   ./scripts/package-release.sh --arch amd64
#   VERSION=v1.0.0 ./scripts/package-release.sh   # name the artifact
#
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUT=${OUT:-$ROOT/dist}
GO=${GO:-go}
VERSION=${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}
ARCHES=()

usage() {
	cat <<'EOF'
Build the self-contained Linux release zip: the gateway for one or both
architectures, the traccar-web build and the Ubuntu installer.

  unzip traccar-server-go_<version>.zip
  cd traccar-server-go_<version>
  sudo ./install.sh

  ./scripts/package-release.sh                  # amd64 and arm64
  ./scripts/package-release.sh --arch amd64
  VERSION=v1.0.0 ./scripts/package-release.sh   # name the artifact

Options:
  --arch ARCH      amd64, arm64, or both (default: both)
  --out DIR        output directory (default: go_server/dist)
  --version NAME   version in the artifact name and -X main.version
  --help           show this help

Environment: OUT, VERSION, GO.
EOF
}

while (($#)); do
	case $1 in
	--arch) ARCHES+=("${2:?--arch needs amd64 or arm64}"); shift 2 ;;
	--out) OUT=${2:?--out needs a directory}; shift 2 ;;
	--version) VERSION=${2:?--version needs a value}; shift 2 ;;
	-h | --help) usage; exit 0 ;;
	*) echo "unknown argument: $1 (try --help)" >&2; exit 2 ;;
	esac
done
((${#ARCHES[@]})) || ARCHES=(amd64 arm64)

for arch in "${ARCHES[@]}"; do
	case $arch in
	amd64 | arm64) ;;
	*) echo "--arch must be amd64 or arm64, not $arch" >&2; exit 2 ;;
	esac
done

[[ -f $ROOT/web/index.html ]] || {
	echo "error: $ROOT/web has no index.html (the traccar-web build)" >&2
	exit 1
}
command -v zip >/dev/null || {
	echo "error: zip is not installed" >&2
	exit 1
}

name="traccar-server-go_${VERSION#v}"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
bundle="$stage/$name"
mkdir -p "$OUT" "$bundle"

install -m 0755 "$ROOT/deploy/ubuntu/install.sh" "$bundle/install.sh"
install -m 0644 "$ROOT/deploy/ubuntu/traccar-gateway.service.in" "$bundle/"
install -m 0644 "$ROOT/deploy/ubuntu/nginx.conf.in" "$bundle/"
install -m 0644 "$ROOT/LICENSE" "$bundle/"
cp -a "$ROOT/web" "$bundle/web"

for arch in "${ARCHES[@]}"; do
	echo "==> building linux/$arch ($VERSION)"
	mkdir -p "$bundle/bin/linux-$arch"
	(
		cd "$ROOT"
		CGO_ENABLED=0 GOOS=linux GOARCH="$arch" "$GO" build -trimpath \
			-ldflags "-s -w -X main.version=$VERSION" \
			-o "$bundle/bin/linux-$arch/traccar-gateway" ./cmd/traccar-gateway
	)
done

cat >"$bundle/INSTALL.txt" <<EOF
traccar-server-go $VERSION

需求：Ubuntu 22.04 / 24.04（systemd）、root 權限。不需要 Go，也不需要原始碼。

安裝（預設 SQLite，Web/API 開在 0.0.0.0:8082）：

    sudo ./install.sh

加上網域與 nginx（gateway 只聽 127.0.0.1:8082）：

    sudo ./install.sh --domain tracker.example.com
    sudo apt-get install -y certbot python3-certbot-nginx
    sudo certbot --nginx -d tracker.example.com

改用 MySQL / PostgreSQL：

    sudo ./install.sh --dsn 'mysql://traccar:密碼@127.0.0.1:3306/traccar'

安裝位置：

    /opt/traccar/bin/traccar-gateway    執行檔
    /opt/traccar/web                    前端（同時是上傳根目錄）
    /opt/traccar/data                   SQLite 資料庫與媒體
    /opt/traccar/conf/traccar.xml            設定檔
    /opt/traccar/conf/api-key                隨機產生的 admin API key

日常操作：

    systemctl status traccar-gateway
    journalctl -u traccar-gateway -f
    curl -H "X-Api-Key: \$(sudo cat /opt/traccar/conf/api-key)" http://127.0.0.1:8082/api/health

瀏覽器開 http://<VPS-IP>:8082/ ，註冊第一個帳號（會成為 administrator）。
設備要連得進來：ufw 與雲端安全組都要開 5013, 5023, 5027, 8610/tcp
（H02 / GT06 / Teltonika / JT808），其餘協議埠見 README。

升級：解壓新版本後再跑一次 sudo ./install.sh；設定檔會保留。
EOF

zipfile="$OUT/$name.zip"
rm -f "$zipfile"
(
	cd "$stage"
	zip -qr -9 "$zipfile" "$name"
)

if command -v sha256sum >/dev/null; then
	hash=$(sha256sum "$zipfile" | awk '{print $1}')
else
	hash=$(shasum -a 256 "$zipfile" | awk '{print $1}')
fi
printf '%s  %s\n' "$hash" "$name.zip" >"$zipfile.sha256"

echo "==> $zipfile"
echo "==> $zipfile.sha256"
du -h "$zipfile" | awk '{print "    size " $1}'
