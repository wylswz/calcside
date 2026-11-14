#!/bin/sh
# First-run compose setup: create docker/.env from .env.example and
# fill in the generated secrets. Safe to re-run — existing .env wins.
set -eu

dir="$(cd "$(dirname "$0")" && pwd)"
env_file="$dir/.env"

if [ ! -f "$env_file" ]; then
  shared_key="$(openssl rand -base64 32)"
  secret_key="$(openssl rand -base64 32)"
  render_key="$(openssl rand -base64 32)"

  sed \
    -e "s|^CALCSIDE_WORKER_SHARED_KEY=$|CALCSIDE_WORKER_SHARED_KEY=$shared_key|" \
    -e "s|^# CALCSIDE_SECRET_KEY=$|CALCSIDE_SECRET_KEY=$secret_key|" \
    -e "s|^CALCSIDE_RENDER_KEY=$|CALCSIDE_RENDER_KEY=$render_key|" \
    "$dir/.env.example" > "$env_file"

  echo "wrote docker/.env (gitignored)"
  exit 0
fi

# Existing .env: only fill a missing render key so older setups pick
# the renderer up by re-running `make docker-env`; every existing
# value still wins.
has_line=$(grep -c '^CALCSIDE_RENDER_KEY=' "$env_file" || true)
has_value=$(grep -c '^CALCSIDE_RENDER_KEY=.' "$env_file" || true)
if [ "$has_value" -gt 0 ]; then
  echo "docker/.env already exists"
elif [ "$has_line" -gt 0 ]; then
  # The assignment exists but is empty — fill it in place so the file
  # never carries the key twice.
  render_key="$(openssl rand -base64 32)"
  tmp="$env_file.tmp.$$"
  awk -v k="$render_key" '/^CALCSIDE_RENDER_KEY=$/ { print "CALCSIDE_RENDER_KEY=" k; next } { print }' "$env_file" > "$tmp" && mv "$tmp" "$env_file"
  echo "filled CALCSIDE_RENDER_KEY in docker/.env"
else
  render_key="$(openssl rand -base64 32)"
  printf '\n# Shared bearer key for the calcside-render service (MCP chart images).\nCALCSIDE_RENDER_KEY=%s\n' "$render_key" >> "$env_file"
  echo "added CALCSIDE_RENDER_KEY to docker/.env"
fi
