#!/usr/bin/env bash
#
# Publish dist/*.zip as a GitHub release, creating the public repository when it
# does not exist yet. Run this outside the sandbox with an authenticated gh:
#
#   gh auth login
#   ./scripts/publish-release.sh onethings/tra-server-go-releases-fortesting v0.1.0
#
# It uploads every dist/*.zip and dist/*.zip.sha256 produced by `make dist`.
set -euo pipefail

REPO=${1:?usage: publish-release.sh <owner/repo> [tag]}
TAG=${2:-v0.1.0}
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

command -v gh >/dev/null || {
	echo "error: gh is not installed (https://cli.github.com/)" >&2
	exit 1
}

shopt -s nullglob
assets=("$ROOT/dist/"*.zip "$ROOT/dist/"*.zip.sha256)
((${#assets[@]})) || {
	echo "error: no artifacts in $ROOT/dist; run 'make dist' first" >&2
	exit 1
}

if gh repo view "$REPO" >/dev/null 2>&1; then
	echo "==> using existing repository $REPO"
else
	echo "==> creating public repository $REPO"
	# --add-readme gives the release a commit to tag on an otherwise empty repo.
	gh repo create "$REPO" --public \
		--description 'traccar-server-go release artifacts' \
		--add-readme
fi

if gh release view "$TAG" --repo "$REPO" >/dev/null 2>&1; then
	echo "==> uploading assets to existing release $TAG"
	gh release upload "$TAG" "${assets[@]}" --repo "$REPO" --clobber
else
	echo "==> creating release $TAG"
	gh release create "$TAG" "${assets[@]}" --repo "$REPO" \
		--title "traccar-server-go $TAG" \
		--notes "Linux binaries for amd64 and arm64, the traccar-web build, and the Ubuntu installer. See INSTALL.txt in the zip."
fi

echo "==> https://github.com/$REPO/releases/tag/$TAG"
