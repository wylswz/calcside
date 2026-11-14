#!/usr/bin/env bash
# Start a disposable API + worker pair for the v2 sandbox comparison.
#
# Requires a FRESH state dir: BENCH_STATE_DIR (or $1) must be empty or absent;
# the default is a new mktemp -d under results/. Refuses occupied ports.
# Records api.pid, worker.pid and connection.json inside the state dir;
# stop later with: down.sh <state-dir>
# Never deletes pre-existing files or databases.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
DIR="$ROOT/benchmark/sandbox-comparison"
RES="$DIR/results"

API_ADDR=${API_ADDR:-127.0.0.1:18787}
WORKER_ADDR=${WORKER_ADDR:-127.0.0.1:18788}
WORKER_KEY=${WORKER_KEY:-bench-shared-key}
SECRET_KEY=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=
# Fake-ip DNS exemption for builtin.block_private_network (make-dev
# convention); real private ranges stay blocked.
NET_ALLOW_CIDRS=${NET_ALLOW_CIDRS:-198.18.0.0/15}
API_PORT=${API_ADDR##*:}

STATE_DIR=${BENCH_STATE_DIR:-${1:-}}
if [ -z "$STATE_DIR" ]; then
  mkdir -p "$RES"
  STATE_DIR=$(mktemp -d "$RES/bench-state-XXXXXXXX")
fi
if [ ! -d "$STATE_DIR" ]; then
  mkdir -p "$STATE_DIR"
fi
STATE_DIR=$(cd "$STATE_DIR" && pwd)

if [ -n "$(ls -A "$STATE_DIR")" ]; then
  echo "up: state dir not empty, refusing to reuse: $STATE_DIR" >&2
  exit 1
fi

port_in_use() {
  lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
}
for addr in "$API_ADDR" "$WORKER_ADDR"; do
  if port_in_use "${addr##*:}"; then
    echo "up: port ${addr##*:} already in use; refusing to start" >&2
    exit 1
  fi
done

# Build the dev binaries (idempotent; Go only, no Vite).
( cd "$ROOT/server" && \
  go build -o "$ROOT/bin/calcside-dev" ./cmd/calcside && \
  go build -o "$ROOT/bin/calcside-worker-dev" ./cmd/calcside-worker )

DSN="$STATE_DIR/bench.db"
( cd "$ROOT/server" && ATLAS_DB_URL="sqlite://$DSN" atlas migrate apply --env sqlite >/dev/null )

cd "$ROOT"
nohup bin/calcside-worker-dev serve \
  --listen "$WORKER_ADDR" --node-id bench-w1 --shared-key "$WORKER_KEY" \
  --api-addr "http://$API_ADDR" --net-allow-cidrs="$NET_ALLOW_CIDRS" \
  >"$STATE_DIR/worker.log" 2>&1 &
echo $! >"$STATE_DIR/worker.pid"

nohup bin/calcside-dev serve --dev \
  --addr "$API_ADDR" --console-origin "http://localhost:$API_PORT" \
  --policy-dir server/policies/examples --secret-key "$SECRET_KEY" \
  --dsn "$DSN" --ext-local-roots contrib --net-allow-cidrs="$NET_ALLOW_CIDRS" \
  --workers "bench-w1=$WORKER_ADDR" --worker-key "$WORKER_KEY" \
  >"$STATE_DIR/api.log" 2>&1 &
echo $! >"$STATE_DIR/api.pid"

wait_health() {
  local url=$1 name=$2
  for _ in $(seq 1 240); do
    if curl -sf "$url/healthz" >/dev/null 2>&1; then return 0; fi
    sleep 0.25
  done
  echo "sandbox-comparison: $name did not become healthy" >&2
  tail -20 "$STATE_DIR/$name.log" >&2 || true
  return 1
}

wait_health "http://$WORKER_ADDR" worker
wait_health "http://$API_ADDR" api

API_PID=$(cat "$STATE_DIR/api.pid")
WORKER_PID=$(cat "$STATE_DIR/worker.pid")
GIT_SHA=$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)
cat >"$STATE_DIR/connection.json" <<EOF
{
  "api": "http://$API_ADDR",
  "worker": "http://$WORKER_ADDR",
  "worker_node_id": "bench-w1",
  "auth": "dev anonymous (--dev); X-Requested-With: calcside header used by csclient.py",
  "dsn": "$(basename "$DSN") (sqlite, same dir)",
  "state_dir": "$STATE_DIR",
  "api_pid": $API_PID,
  "worker_pid": $WORKER_PID,
  "logs": ["api.log", "worker.log"],
  "net_allow_cidrs": "$NET_ALLOW_CIDRS",
  "note": "--net-allow-cidrs exempts the host's fake-ip DNS range (198.18.x.x) from builtin.block_private_network; real private ranges stay blocked",
  "binaries": "bin/calcside-dev + bin/calcside-worker-dev built from HEAD $GIT_SHA at up time; tracked Go tree may be dirty, benchmark state is untracked"
}
EOF
echo "up: api=$API_ADDR worker=$WORKER_ADDR state=$STATE_DIR"
