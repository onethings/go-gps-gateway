#!/usr/bin/env bash
# Proves the reference-schema attach path end to end, against a real PostgreSQL
# database that was created from a Traccar Java dump:
#
#   ./scripts/compat-e2e.sh                      # throwaway postgres on 55442
#   DSN='postgres://user:pass@host:5432/traccar?sslmode=disable' ./scripts/compat-e2e.sh
#
# The run loads scripts/reference-schema.sql into an empty database, starts the
# gateway with -db-schema reference, and then checks three things: the reference
# devices are readable, their history is readable, and a new position the
# gateway receives is written back to the adopted table.
#
#   COMPAT_E2E_PORT_BASE=19000   move every listener (default 19000)
#   COMPAT_E2E_NO_BUILD=1        use ./bin as is
#   COMPAT_E2E_KEEP_LOG=1        keep the gateway log even on success
set -uo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root" || exit 1

bin_dir=${BIN_DIR:-bin}
gateway=$bin_dir/traccar-gateway
dsn=${DSN:-${TRACCAR_DSN:-postgres://postgres:traccar@127.0.0.1:55442/traccar?sslmode=disable}}
port_base=${COMPAT_E2E_PORT_BASE:-19000}
api_key=${COMPAT_E2E_API_KEY:-compat-e2e-admin}
api="http://127.0.0.1:$((port_base + 82))"
osmand="127.0.0.1:$((port_base + 55))"
container=${COMPAT_E2E_CONTAINER:-traccar-pg-compat}
log=$(mktemp "${TMPDIR:-/tmp}/traccar-compat-e2e.XXXXXX")
started_container=""
gateway_pid=""
failures=0

cleanup() {
	if [[ -n $gateway_pid ]]; then
		kill "$gateway_pid" 2>/dev/null
		wait "$gateway_pid" 2>/dev/null
	fi
	if [[ -n $started_container ]]; then
		docker rm -f "$started_container" >/dev/null 2>&1
	fi
	if [[ ${COMPAT_E2E_KEEP_LOG:-0} != 1 && $failures -eq 0 ]]; then
		rm -f "$log"
	fi
}
trap cleanup EXIT

fail() {
	echo "FAIL $*"
	failures=$((failures + 1))
}

ok() {
	echo "ok   $*"
}

psql_run() { # < file
	if [[ -n $started_container ]]; then
		docker exec -i "$started_container" psql -v ON_ERROR_STOP=1 -U postgres -d traccar
	else
		psql -v ON_ERROR_STOP=1 "$dsn"
	fi
}

query() { # <sql>
	if [[ -n $started_container ]]; then
		docker exec -i "$started_container" psql -tAq -U postgres -d traccar -c "$1"
	else
		psql -tAq "$dsn" -c "$1"
	fi
}

for tool in curl jq; do
	if ! command -v "$tool" >/dev/null; then
		echo "missing $tool; install it or run this on a developer machine" >&2
		exit 1
	fi
done

if [[ -z ${DSN:-}${TRACCAR_DSN:-} ]]; then
	if ! command -v docker >/dev/null; then
		echo "no DSN and no docker; pass DSN=postgres://..." >&2
		exit 1
	fi
	if ! docker info >/dev/null 2>&1; then
		echo "no DSN and the docker daemon is unreachable; pass DSN=postgres://..." >&2
		exit 1
	fi
	echo "==> starting a throwaway PostgreSQL (port 55442)"
	docker rm -f "$container" >/dev/null 2>&1
	docker run --rm -d --name "$container" -p 55442:5432 \
		-e POSTGRES_PASSWORD=traccar -e POSTGRES_DB=traccar postgres:16 >/dev/null || exit 1
	started_container=$container
	for _ in $(seq 1 60); do
		if docker exec "$container" pg_isready -U postgres -d traccar >/dev/null 2>&1; then
			break
		fi
		sleep 0.5
	done
fi

echo "==> loading the reference schema"
if ! psql_run <scripts/reference-schema.sql >/dev/null; then
	echo "could not load scripts/reference-schema.sql into $dsn" >&2
	exit 1
fi

if [[ ${COMPAT_E2E_NO_BUILD:-0} != 1 ]]; then
	echo "==> building"
	mkdir -p "$bin_dir"
	go build -o "$gateway" ./cmd/traccar-gateway || exit 1
fi
if [[ ! -x $gateway ]]; then
	echo "missing $gateway; run make build first" >&2
	exit 1
fi

echo "==> starting the gateway with -db-schema reference"
"$gateway" \
	-dsn "$dsn" \
	-db-schema reference \
	-api-key "$api_key" \
	-osmand "$osmand" \
	-http "127.0.0.1:$((port_base + 82))" \
	-batch-interval 200ms \
	>"$log" 2>&1 &
gateway_pid=$!

ready=""
for _ in $(seq 1 100); do
	if curl -fsS -o /dev/null "$api/api/health"; then
		ready=1
		break
	fi
	kill -0 "$gateway_pid" 2>/dev/null || break
	sleep 0.2
done
if [[ -z $ready ]]; then
	echo "the gateway did not answer /api/health:" >&2
	sed 's/^/    /' "$log" >&2
	exit 1
fi

get() { curl -sS -H "X-Api-Key: $api_key" "$api$1"; }

adopted=$(query "SELECT to_regclass('tc_devices') IS NULL AND to_regclass('devices') IS NOT NULL")
if [[ $adopted == "t" ]]; then
	ok "the reference tables were adopted under the Go names"
else
	fail "tc_devices still exists or devices does not; adoption did not run"
fi

devices=$(get "/api/devices?limit=1000")
truck=$(jq -r 'map(select(.uniqueId == "862170041234567")) | .[0] // {}' <<<"$devices")
if [[ $(jq -r '.name // ""' <<<"$truck") == "Truck 1" ]]; then
	ok "device 862170041234567 is readable with its reference name"
else
	fail "device 862170041234567 is missing or renamed: $truck"
fi
if [[ $(jq -r '.model // ""' <<<"$truck") == "GT06N" ]]; then
	ok "the reference model column came across"
else
	fail "model = $(jq -r '.model // ""' <<<"$truck"), want GT06N"
fi
spare=$(jq -r 'map(select(.uniqueId == "862170041234568")) | .[0] // {}' <<<"$devices")
if [[ $(jq -r '.disabled // false' <<<"$spare") == "true" ]]; then
	ok "the reference disabled flag came across"
else
	fail "device 862170041234568 is not disabled: $spare"
fi

device_id=$(jq -r '.id // 0' <<<"$truck")
history=$(get "/api/positions?deviceId=$device_id&limit=100")
if [[ $(jq -r 'length' <<<"$history") -eq 2 ]]; then
	ok "the two reference positions are readable"
else
	fail "history has $(jq -r 'length' <<<"$history") rows, want 2"
fi
geofences=$(jq -r '.[-1].geofenceIds // [] | join(",")' <<<"$history")
if [[ $geofences == "3,4" ]]; then
	ok "the comma separated geofence ids became a JSON array"
else
	fail "geofence ids = $geofences, want 3,4"
fi

echo "==> writing a new position through the OsmAnd endpoint"
curl -fsS "http://$osmand/?id=862170041234567&lat=54.95000&lon=24.03000&speed=12&timestamp=$(date +%s)" \
	-o /dev/null || fail "the OsmAnd request failed"

written=""
for _ in $(seq 1 50); do
	written=$(query "SELECT count(*) FROM positions WHERE device_id = $device_id")
	if [[ $written -ge 3 ]]; then
		break
	fi
	sleep 0.2
done
if [[ $written -ge 3 ]]; then
	ok "the gateway wrote a position into the adopted table"
else
	fail "positions for device $device_id = $written, want 3"
fi

where=$(query "SELECT latitude || ',' || longitude FROM positions WHERE device_id = $device_id ORDER BY id DESC LIMIT 1")
if [[ $where == 54.95,24.03 ]]; then
	ok "the new row carries the reported coordinates"
else
	fail "newest coordinates = $where, want 54.95,24.03"
fi

fix=$(query "SELECT (fixtime IS NOT NULL)::text FROM positions WHERE device_id = $device_id ORDER BY id DESC LIMIT 1")
if [[ $fix == "true" ]]; then
	ok "the reference fixtime column was filled from the device time"
else
	fail "fixtime is null on the row the gateway wrote"
fi

if [[ $failures -gt 0 ]]; then
	echo "$failures check(s) failed; gateway log follows" >&2
	sed 's/^/    /' "$log" >&2
	exit 1
fi
echo "==> reference schema attach verified"
