#!/usr/bin/env bash
# Stop the API/worker pair recorded in a state dir by up.sh.
#
# Usage: down.sh <state-dir>   (or BENCH_STATE_DIR=<dir> down.sh)
# Each recorded pid is verified against its expected command line before
# killing; anything unexpected is reported, never killed. State files
# (db, logs, connection.json) are kept. This script never touches the
# shared OpenShell gateway or any other process.
set -uo pipefail

STATE_DIR=${1:-${BENCH_STATE_DIR:-}}
if [ -z "$STATE_DIR" ]; then
  echo "usage: down.sh <state-dir> | BENCH_STATE_DIR=<dir> down.sh" >&2
  exit 2
fi
if [ ! -d "$STATE_DIR" ]; then
  echo "down: no such state dir: $STATE_DIR" >&2
  exit 1
fi

killed=""
for spec in "api:calcside-dev serve" "worker:calcside-worker-dev serve"; do
  name=${spec%%:*}
  expect=${spec#*:}
  pidfile="$STATE_DIR/$name.pid"
  [ -f "$pidfile" ] || continue
  pid=$(cat "$pidfile")
  case "$pid" in
    ''|*[!0-9]*)
      echo "down: $name pid file corrupt: '$pid'" >&2
      rm -f "$pidfile"
      continue
      ;;
  esac
  cmd=$(ps -p "$pid" -o command= 2>/dev/null || true)
  if [ -z "$cmd" ]; then
    rm -f "$pidfile"   # already gone
    continue
  fi
  case "$cmd" in
    *"$expect"*)
      if kill "$pid" 2>/dev/null; then
        killed="$killed $pid"
      else
        echo "down: kill $name pid $pid failed" >&2
      fi
      ;;
    *)
      echo "down: $name pid $pid command mismatch, not killing: $cmd" >&2
      ;;
  esac
  rm -f "$pidfile"
done

# Bounded wait for killed processes to exit.
for _ in $(seq 1 40); do
  gone=1
  for pid in $killed; do
    kill -0 "$pid" 2>/dev/null && gone=0
  done
  [ "$gone" = 1 ] && break
  sleep 0.25
done
for pid in $killed; do
  kill -0 "$pid" 2>/dev/null && echo "down: pid $pid still running" >&2
done
echo "down: state=$STATE_DIR (verified pids stopped; state files kept)"
