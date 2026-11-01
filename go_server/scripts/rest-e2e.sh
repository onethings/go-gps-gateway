#!/usr/bin/env bash
# Drives the whole REST chain against a real PostgreSQL: device frames land in
# the positions table, the alert rules write alerts, and a command posted while
# the device is offline is delivered when that device reconnects.
#
#   make db-up && ./scripts/rest-e2e.sh        # throwaway postgres on 55432
#   DSN='postgres://user:pass@host:5432/traccar?sslmode=disable' ./scripts/rest-e2e.sh
#
# The run is repeatable: devices are looked up by uniqueId and the assertions
# compare "newest row id before" with "newest row id after" instead of expecting
# an empty database.
#
#   REST_E2E_SCENES="gt06 h02"   replay only these scenes
#   REST_E2E_PORT_BASE=26000     move every listener (default 18000)
#   REST_E2E_NO_BUILD=1          use ./bin as is
#   REST_E2E_KEEP_LOG=1          keep the gateway log even on success
set -uo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root" || exit 1

bin_dir=${BIN_DIR:-bin}
gateway=$bin_dir/traccar-gateway
simulator=$bin_dir/device-sim
dsn=${DSN:-${TRACCAR_DSN:-postgres://postgres:traccar@127.0.0.1:55432/traccar?sslmode=disable}}
api_key=${REST_E2E_API_KEY:-rest-e2e-admin}
port_base=${REST_E2E_PORT_BASE:-18000}
scenes=${REST_E2E_SCENES:-"gt06 h02 jt808 teltonika"}
overspeed=${REST_E2E_OVERSPEED_KNOTS:-1}
api="http://127.0.0.1:$((port_base + 82))"
log=$(mktemp "${TMPDIR:-/tmp}/traccar-rest-e2e.XXXXXX")

gateway_pid=""
failures=0

cleanup() {
	if [[ -n $gateway_pid ]]; then
		kill "$gateway_pid" 2>/dev/null
		wait "$gateway_pid" 2>/dev/null
	fi
	if [[ ${REST_E2E_KEEP_LOG:-0} != 1 && $failures -eq 0 ]]; then
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

port_for() {
	case $1 in
	h02) echo $((port_base + 13)) ;;
	gt06) echo $((port_base + 23)) ;;
	teltonika) echo $((port_base + 27)) ;;
	jt808) echo $((port_base + 610)) ;;
	*) echo "" ;;
	esac
}

api_url() { printf '%s%s' "$api" "$1"; }

get() { curl -sS -H "X-Api-Key: $api_key" "$(api_url "$1")"; }

# post stores the response body and status in $body / $http_code. It does not
# print them: the caller runs it outside a command substitution so both values
# survive.
post() {
	local response
	response=$(curl -sS -X POST -H "X-Api-Key: $api_key" \
		-H 'Content-Type: application/json' -d "$2" \
		-w $'\n%{http_code}' "$(api_url "$1")")
	http_code=${response##*$'\n'}
	body=${response%$'\n'*}
}

device_id() { # uniqueId protocol
	get "/api/devices?protocol=$2&limit=1000" |
		jq -r --arg u "$1" '(.devices // []) | map(select(.uniqueId == $u)) | (.[0].id // 0)'
}

newest_position_id() { # deviceId
	if [[ $1 -le 0 ]]; then
		echo 0
		return
	fi
	get "/api/positions?deviceId=$1&limit=1" | jq -r '(.positions // [])[0].id // 0'
}

newest_alert_id() {
	get "/api/alerts?limit=1" | jq -r '(.alerts // [])[0].id // 0'
}

command_status() { # deviceId
	get "/api/commands?deviceId=$1&limit=1" |
		jq -r '(.commands // [])[0] | if . == null then "-" else "\(.status) \(.attempts)" end'
}

for tool in curl jq; do
	if ! command -v "$tool" >/dev/null; then
		echo "missing $tool; install it or run this on a developer machine" >&2
		exit 1
	fi
done

if [[ ${REST_E2E_NO_BUILD:-0} != 1 ]]; then
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

echo "==> starting gateway with -dsn (ports ${port_base}+, REST $api)"
"$gateway" \
	-dsn "$dsn" \
	-api-key "$api_key" \
	-h02 "127.0.0.1:$(port_for h02)" \
	-gt06 "127.0.0.1:$(port_for gt06)" \
	-teltonika "127.0.0.1:$(port_for teltonika)" \
	-jt808 "127.0.0.1:$(port_for jt808)" \
	-http "127.0.0.1:$((port_base + 82))" \
	-overspeed-knots "$overspeed" \
	-batch-interval 200ms \
	-command-retry 10s \
	>"$log" 2>&1 &
gateway_pid=$!

ready=""
for _ in $(seq 1 100); do
	if curl -fsS -o /dev/null "$(api_url /api/health)"; then
		ready=1
		break
	fi
	kill -0 "$gateway_pid" 2>/dev/null || break
	sleep 0.2
done
if [[ -z $ready ]]; then
	echo "gateway did not answer /api/health:" >&2
	sed 's/^/    /' "$log" >&2
	if grep -q 'operation not permitted' "$log"; then
		echo "hint: this shell cannot bind TCP ports; run the e2e outside the sandbox" >&2
	fi
	echo "hint: DSN was $dsn; start PostgreSQL with 'make db-up' or pass DSN=..." >&2
	exit 1
fi

alerts_before=$(newest_alert_id)
registered=0
gt06_id=0

for scene in $scenes; do
	scene_file="internal/devicescene/scenes/$scene.json"
	if [[ ! -f $scene_file ]]; then
		fail "$scene: no scene file at $scene_file"
		continue
	fi
	unique_id=$(jq -r '.uniqueId // ""' "$scene_file")
	protocol=$(jq -r '.protocol // ""' "$scene_file")
	expected=$(jq -r '.positions // 1' "$scene_file")

	id=$(device_id "$unique_id" "$protocol")
	before=$(newest_position_id "$id")

	if ! "$simulator" -addr "127.0.0.1:$(port_for "$scene")" -scene "$scene" >/dev/null 2>&1; then
		fail "$scene: simulator could not replay the scene"
		continue
	fi

	if [[ $id -le 0 ]]; then
		for _ in $(seq 1 50); do
			id=$(device_id "$unique_id" "$protocol")
			[[ $id -gt 0 ]] && break
			sleep 0.2
		done
	fi
	if [[ $id -le 0 ]]; then
		fail "$scene: device $unique_id was not registered by -auto-register"
		continue
	fi
	registered=$((registered + 1))
	if [[ $protocol == gt06 ]]; then
		gt06_id=$id
	fi

	after=0
	for _ in $(seq 1 50); do
		after=$(newest_position_id "$id")
		[[ $after -gt $before ]] && break
		sleep 0.2
	done
	if [[ $after -le $before ]]; then
		fail "$scene: /api/positions has no new row for device $id"
		continue
	fi

	count=$(get "/api/positions?deviceId=$id&limit=1000" | jq -r '.count // 0')
	if [[ $count -lt $expected ]]; then
		fail "$scene: /api/positions returned $count row(s), scene records $expected"
		continue
	fi
	protocol_seen=$(get "/api/positions?deviceId=$id&limit=1" | jq -r '(.positions // [])[0].protocol // ""')
	if [[ -n $protocol && $protocol_seen != "$protocol" ]]; then
		fail "$scene: newest position protocol is $protocol_seen, want $protocol"
		continue
	fi
	ok "$scene: $count position(s) via device $id (HTTP $((port_base + 82)))"
done

if [[ $registered -eq 0 ]]; then
	echo "==> no scene registered a device; skipping the alert and command checks"
	sed 's/^/    /' "$log" >&2
	exit 1
fi

alert_after=$alerts_before
for _ in $(seq 1 50); do
	alert_after=$(newest_alert_id)
	[[ $alert_after -gt $alerts_before ]] && break
	sleep 0.2
done
if [[ $alert_after -le $alerts_before ]]; then
	fail "no alert was written for the replayed positions (threshold ${overspeed} knots)"
else
	alert_type=$(get "/api/alerts?limit=1" | jq -r '(.alerts // [])[0].type // ""')
	if [[ $alert_type != deviceOverspeed ]]; then
		fail "newest alert type is $alert_type, want deviceOverspeed"
	else
		ok "alerts: /api/alerts returned the new $alert_type row (id $alert_after)"
	fi
fi

gt06_id=${gt06_id:-0}
if [[ $gt06_id -le 0 ]]; then
	fail "commands: the gt06 scene was not replayed, so there is no device to command"
else
	queued=""
	for _ in 1 2 3; do
		post /api/commands "{\"deviceId\":$gt06_id,\"type\":\"engineStop\"}"
		if [[ $http_code != 202 ]]; then
			fail "commands: POST /api/commands returned $http_code ($body)"
			break
		fi
		queued=$(printf '%s' "$body" | jq -r '.queued // false')
		if [[ $queued == true ]]; then
			break
		fi
		sleep 0.5
	done
	if [[ $queued != true ]]; then
		fail "commands: device $gt06_id was still online, the command was never queued"
	else
		state=$(command_status "$gt06_id")
		if [[ $state != "pending 0" ]]; then
			fail "commands: status after POST is '$state', want 'pending 0'"
		else
			ok "commands: POST /api/commands queued a pending engineStop for device $gt06_id"

			# The device reconnects: the dispatcher replays pending rows.
			"$simulator" -addr "127.0.0.1:$(port_for gt06)" -scene gt06 >/dev/null 2>&1
			state="-"
			for _ in $(seq 1 75); do
				state=$(command_status "$gt06_id")
				[[ ${state%% *} != pending ]] && break
				sleep 0.2
			done
			case $state in
			"sent "*) ok "commands: device reconnected and the command reached the socket ($state)" ;;
			done*) ok "commands: device reconnected, received and acknowledged the command ($state)" ;;
			*) fail "commands: status after reconnect is '$state', want sent or done" ;;
			esac
		fi
	fi
fi

if [[ $failures -ne 0 ]]; then
	echo "==> $failures check(s) failed; gateway log ($log):"
	sed 's/^/    /' "$log"
	exit 1
fi

echo "==> rest e2e passed: positions, alerts and commands all reached PostgreSQL through the REST API"
