#!/usr/bin/env bash
# Replays one bundled scene per protocol against a gateway that has no database,
# and checks that the positions reach the log sink. This is the shortest way to
# prove an install works: it needs no PostgreSQL, no hardware and no root.
#
#   ./scripts/smoke.sh            # gt06, h02, jt808, teltonika
#   SMOKE_PORT_BASE=25000 ./scripts/smoke.sh
#   SMOKE_SCENES="gt06" ./scripts/smoke.sh
set -uo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root" || exit 1

bin_dir=${BIN_DIR:-bin}
gateway=$bin_dir/traccar-gateway
simulator=$bin_dir/device-sim
port_base=${SMOKE_PORT_BASE:-15000}
scenes=${SMOKE_SCENES:-"gt06 h02 jt808 teltonika gt02 gps056"}
log=$(mktemp "${TMPDIR:-/tmp}/traccar-smoke.XXXXXX")

gateway_pid=""
cleanup() {
	[[ -n $gateway_pid ]] && kill "$gateway_pid" 2>/dev/null && wait "$gateway_pid" 2>/dev/null
	rm -f "$log"
}
trap cleanup EXIT

port_for() {
	case $1 in
	h02) echo $((port_base + 13)) ;;
	gt06) echo $((port_base + 23)) ;;
	teltonika) echo $((port_base + 27)) ;;
	jt808) echo $((port_base + 610)) ;;
	gt02) echo $((port_base + 22)) ;;
	gps056) echo $((port_base + 141)) ;;
	*) echo "" ;;
	esac
}

if [[ ${SMOKE_NO_BUILD:-0} != 1 ]]; then
	echo "==> building"
	mkdir -p "$bin_dir"
	go build -o "$gateway" ./cmd/traccar-gateway || exit 1
	go build -o "$simulator" ./cmd/device-sim || exit 1
fi
for binary in "$gateway" "$simulator"; do
	if [[ ! -x $binary ]]; then
		echo "missing $binary; run make build first" >&2
		exit 1
	fi
done

echo "==> starting gateway on ports $port_base+ (no database, log sink)"
"$gateway" \
	-h02 "127.0.0.1:$(port_for h02)" \
	-gt06 "127.0.0.1:$(port_for gt06)" \
	-teltonika "127.0.0.1:$(port_for teltonika)" \
	-jt808 "127.0.0.1:$(port_for jt808)" \
	-gt02 "127.0.0.1:$(port_for gt02)" \
	-gps056 "127.0.0.1:$(port_for gps056)" \
	-http "" \
	>"$log" 2>&1 &
gateway_pid=$!

ready=""
for _ in $(seq 1 100); do
	if grep -q 'gt06: listening' "$log"; then
		ready=1
		break
	fi
	kill -0 "$gateway_pid" 2>/dev/null || break
	grep -q 'operation not permitted' "$log" && break
	sleep 0.1
done

if [[ -z $ready ]]; then
	echo "gateway did not start:" >&2
	sed 's/^/    /' "$log" >&2
	if grep -q 'operation not permitted' "$log"; then
		echo "hint: this shell cannot bind TCP ports; run the smoke test outside the sandbox" >&2
	fi
	exit 1
fi

failures=0
total=0
for scene in $scenes; do
	address="127.0.0.1:$(port_for "$scene")"
	expected=$(grep -o '"positions": *[0-9]*' "internal/devicescene/scenes/$scene.json" 2>/dev/null | grep -o '[0-9]*')
	before=$(grep -c 'position {' "$log")
	if ! "$simulator" -addr "$address" -scene "$scene" >/dev/null 2>&1; then
		echo "FAIL $scene: simulator could not replay the scene"
		failures=$((failures + 1))
		continue
	fi
	sleep 0.3
	after=$(grep -c 'position {' "$log")
	got=$((after - before))
	total=$((total + got))
	if [[ $got -lt ${expected:-1} ]]; then
		echo "FAIL $scene: $got position(s) reached the sink, want ${expected:-1}"
		failures=$((failures + 1))
	else
		echo "ok   $scene: $got position(s) decoded and logged"
	fi
done

if [[ $failures -ne 0 ]]; then
	echo "==> $failures scene(s) failed; gateway log:"
	sed 's/^/    /' "$log"
	exit 1
fi

printf '==> smoke test passed: %d position(s) across %d protocol(s)\n' "$total" "$(echo $scenes | wc -w | tr -d ' ')"
