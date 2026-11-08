#!/usr/bin/env bash
# Run one chapter's tests (or all) with the race detector and a timeout.   scripts/test.sh 07   |   scripts/test.sh all
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "${1:-all}" = "all" ]; then
  exec go test ./... -race -timeout 300s
fi
n=$(printf '%02d' "$((10#$1))")
exec go test ./internal/... -race -timeout 120s -run "Ch${n}_" -v
