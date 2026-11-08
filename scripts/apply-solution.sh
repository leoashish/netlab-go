#!/usr/bin/env bash
# Copy a chapter's reference solution over your files.   THIS OVERWRITES YOUR WORK: commit or stash first.
#   scripts/apply-solution.sh 05            apply chapter 05
#   scripts/apply-solution.sh --through 05  apply chapters 01..05 in order
#   scripts/apply-solution.sh --list        list the chapters that have solutions
set -euo pipefail
cd "$(dirname "$0")/.."

apply_one() {
  local n; n=$(printf '%02d' "$((10#$1))")
  local dir="_solutions/ch$n"
  [ -d "$dir" ] || { echo "no solution for chapter $n (expected $dir)" >&2; exit 1; }
  cp -r "$dir"/. .
  echo "applied the reference solution of chapter $n"
}

case "${1:-}" in
  --list) ls -d _solutions/ch* | sed 's#_solutions/ch##' | tr '\n' ' '; echo ;;
  --through) [ -n "${2:-}" ] || { echo "usage: $0 --through NN" >&2; exit 2; }
             for i in $(seq 1 "$((10#$2))"); do apply_one "$i"; done ;;
  "") echo "usage: $0 NN | --through NN | --list" >&2; exit 2 ;;
  *) apply_one "$1" ;;
esac
