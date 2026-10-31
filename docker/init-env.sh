#!/bin/sh
# First-run compose setup: create docker/.env from .env.example and
# fill in the generated secrets. Safe to re-run — existing .env wins.
set -eu

dir="$(cd "$(dirname "$0")" && pwd)"
env_file="$dir/.env"

if [ -f "$env_file" ]; then
  echo "docker/.env already exists"
  exit 0
fi

shared_key="$(openssl rand -base64 32)"
secret_key="$(openssl rand -base64 32)"

sed \
  -e "s|^CALCSIDE_WORKER_SHARED_KEY=$|CALCSIDE_WORKER_SHARED_KEY=$shared_key|" \
  -e "s|^# CALCSIDE_SECRET_KEY=$|CALCSIDE_SECRET_KEY=$secret_key|" \
  "$dir/.env.example" > "$env_file"

echo "wrote docker/.env (gitignored)"
