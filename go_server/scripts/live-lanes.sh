#!/usr/bin/env bash
# Replays one PR-1 lane against a gateway that is already listening.
#
#   ./scripts/live-lanes.sh 2
#
# The gateway must be started per the plan's boot recipe. Lanes that count rows
# need PSQL_DSN, lanes that read counters need API_URL. Without them the lane
# reports skip and exits 3 so a caller can tell it apart from a failure.
set -uo pipefail

lane=${1:-}
if [[ -z "$lane" ]]; then
	echo "usage: $(basename "$0") <lane>" >&2
	exit 2
fi

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
out=/tmp/swarm-pr1
mkdir -p "$out"
log="$out/lane-$lane.log"
: >"$log"

host=${SIM_HOST:-127.0.0.1}
psql_dsn=${PSQL_DSN:-}
api_url=${API_URL:-http://$host:8082}

sim() {
	(cd "$root" && go run ./cmd/device-sim "$@") >>"$log" 2>&1
}

need_db() {
	if [[ -z "$psql_dsn" ]]; then
		echo "skip lane $lane needs PSQL_DSN" | tee -a "$log"
		exit 3
	fi
}

count_sql() {
	psql "$psql_dsn" -Atc "$1" 2>>"$log"
}

pass() {
	echo "pass $lane" | tee -a "$log"
	exit 0
}

fail() {
	echo "fail $lane $*" | tee -a "$log"
	exit 1
}

case "$lane" in
1)
	# Trunk has no simulator, so this lane gates the end state instead: two
	# positions reach the table.
	need_db
	before=$(count_sql "select count(*) from positions") || fail "count before"
	sim -addr "$host:5023" -scene gt06 || fail "sim"
	after=$(count_sql "select count(*) from positions") || fail "count after"
	[[ $((after - before)) -eq 2 ]] || fail "positions grew by $((after - before)), want 2"
	pass
	;;
2)
	sim -addr "$host:5023" -scene gt06 || fail "sim"
	grep -q '^gt06 conn 1 recv 1 replies' "$log" || fail "no reply to the login"
	grep -q 'recv 7878050100' "$log" || fail "login acknowledgement missing"
	pass
	;;
3)
	sim -addr "$host:5013" -scene h02 || fail "sim"
	pass
	;;
4)
	sim -addr "$host:8610" -scene jt808 || fail "sim"
	pass
	;;
5)
	sim -addr "$host:5027" -scene teltonika || fail "sim"
	pass
	;;
6)
	sim -addr "$host:5023" -scene gt06 -mode split || fail "sim"
	need_db
	grew=$(count_sql "select count(*) from positions where device_id = (select id from devices where unique_id = '864717003283581')") ||
		fail "count"
	[[ "$grew" -ge 1 ]] || fail "split frame produced no position"
	pass
	;;
7)
	sim -addr "$host:5023" -scene gt06 -mode batch || fail "sim"
	need_db
	grew=$(count_sql "select count(*) from positions where device_id = (select id from devices where unique_id = '864717003283581')") ||
		fail "count"
	[[ "$grew" -ge 1 ]] || fail "batched frames produced no position"
	pass
	;;
8)
	need_db
	before=$(count_sql "select count(*) from devices") || fail "count before"
	sim -addr "$host:5023" -scene gt06-unknown -read-wait 200ms || fail "sim"
	after=$(count_sql "select count(*) from devices") || fail "count after"
	[[ "$after" -eq "$before" ]] || fail "gateway registered an unknown identifier"
	pass
	;;
9)
	need_db
	for _ in 1 2 3; do
		sim -addr "$host:5023" -scene gt06 -read-wait 100ms || fail "sim"
	done
	rows=$(count_sql "select count(*) from devices where unique_id = '864717003283581'") || fail "count"
	[[ "$rows" -eq 1 ]] || fail "devices has $rows rows for the identifier, want 1"
	pass
	;;
10)
	sim -addr "$host:5023" -scene gt06 -connections 200 -repeat 10 -read-wait 200ms ||
		fail "sim"
	errors=$(curl -sf "$api_url/metrics" | awk '/^traccar_decode_errors_total/ {sum += $2} END {print sum + 0}') ||
		fail "metrics"
	[[ "$errors" -eq 0 ]] || fail "decode_errors total is $errors, want 0"
	active=$(curl -sf "$api_url/metrics" | awk '/^traccar_connections_active/ {sum += $2} END {print sum + 0}') ||
		fail "metrics"
	[[ "$active" -eq 0 ]] || fail "connections_active is $active, want 0"
	pass
	;;
*)
	echo "unknown lane $lane" >&2
	exit 2
	;;
esac
