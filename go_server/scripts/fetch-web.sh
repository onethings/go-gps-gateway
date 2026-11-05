#!/usr/bin/env bash
# Installs a traccar-web build into the directory the gateway serves, or checks
# that the installed one is complete.
#
#   scripts/fetch-web.sh --from /path/to/traccar-web/build
#   scripts/fetch-web.sh --from /path/to/traccar-web-6.7.1.zip
#   scripts/fetch-web.sh --check
#
# The build has to be produced elsewhere: this repository stores the result, not
# the frontend sources, so a checkout keeps building without node or a network.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DEST=$ROOT/web
SOURCE=
CHECK_ONLY=0
WORK=
UNPACKED=

cleanup() {
	[[ -n $WORK && -d $WORK ]] && rm -rf "$WORK"
	return 0
}
trap cleanup EXIT

die() { echo "fetch-web: $*" >&2; exit 1; }

usage() {
	sed -n '2,9p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

while [[ $# -gt 0 ]]; do
	case $1 in
		--from) SOURCE=${2:?--from needs a directory or a zip}; shift 2 ;;
		--dest) DEST=${2:?--dest needs a directory}; shift 2 ;;
		--check) CHECK_ONLY=1; shift ;;
		-h | --help) usage; exit 0 ;;
		*) die "unknown argument $1" ;;
	esac
done

[[ -d $DEST ]] || DEST=$(mkdir -p "$DEST" && cd "$DEST" && pwd)

# install_build copies one build over the current tree. node_modules survives
# because the mapbox rtl-text file the build loads at runtime lives there.
install_build() {
	local source=$1
	[[ -f $source/index.html ]] || die "$source has no index.html; point --from at a traccar-web build"
	[[ -d $source/assets ]] || die "$source has no assets directory"

	find "$DEST" -mindepth 1 -maxdepth 1 ! -name node_modules -exec rm -rf {} +
	cp -a "$source/." "$DEST/"
	echo "installed $(find "$DEST/assets" -type f | wc -l | tr -d ' ') assets into $DEST"
}

# unpack_zip extracts a release zip and points UNPACKED at the directory that
# holds index.html, which a release archive may wrap in one more level.
unpack_zip() {
	local archive=$1
	WORK=$(mktemp -d "${TMPDIR:-/tmp}/traccar-web.XXXXXX")

	command -v unzip >/dev/null 2>&1 || die "unzip is needed to read $archive"
	unzip -q "$archive" -d "$WORK"

	local index
	index=$(find "$WORK" -name index.html -maxdepth 4 | head -1)
	[[ -n $index ]] || die "$archive has no index.html"
	UNPACKED=$(dirname "$index")
}

# check_build fails when the document points at an asset that is not there.
check_build() {
	local missing=0
	local reference path

	[[ -f $DEST/index.html ]] || die "$DEST has no index.html; run: make web WEB_SRC=/path/to/traccar-web/build"
	[[ -d $DEST/assets ]] || die "$DEST has no assets directory"
	if [[ -z $(find "$DEST/assets" -type f -maxdepth 1 | head -1) ]]; then
		die "$DEST/assets is empty"
	fi

	while IFS= read -r reference; do
		path=${reference#./}
		if [[ ! -f $DEST/$path ]]; then
			echo "fetch-web: index.html references a missing file: $path" >&2
			missing=1
		fi
	done < <(grep -oE '(src|href)="\./[^"]+"' "$DEST/index.html" | sed -E 's/.*="\.\/(.*)"/\1/')

	for placeholder in '${title}' '${description}' '${colorPrimary}'; do
		if ! grep -qF "$placeholder" "$DEST/index.html"; then
			echo "fetch-web: note: index.html has no $placeholder placeholder" >&2
		fi
	done

	[[ $missing -eq 0 ]] || exit 1
	echo "web build ok: index.html and $(find "$DEST/assets" -type f | wc -l | tr -d ' ') assets in $DEST"
}

if [[ $CHECK_ONLY -eq 1 ]]; then
	check_build
	exit 0
fi

[[ -n $SOURCE ]] || { usage; die "--from or --check is required"; }
[[ -e $SOURCE ]] || die "$SOURCE does not exist"

case $SOURCE in
	*.zip)
		unpack_zip "$SOURCE"
		install_build "$UNPACKED"
		;;
	*) install_build "$SOURCE" ;;
esac

check_build
