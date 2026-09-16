#!/usr/bin/env bash
# Run every end-to-end scenario. This is what `make e2e` and CI invoke.
#
# The suite is hermetic: stub `vb` and `herdr` executables mean it never touches
# the developer's Herdr session, never starts an agent, and never makes a
# provider call.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
repo_root="$(cd "$here/.." && pwd)"

if [ ! -x "${HVB_BIN:-$repo_root/bin/hvb}" ]; then
  echo "run-all.sh: build hvb first (make build)" >&2
  exit 1
fi

failed=()
passed=0

for scenario in "$here"/[0-9][0-9]-*.sh; do
  [ -e "$scenario" ] || continue
  if bash "$scenario"; then
    passed=$((passed + 1))
  else
    failed+=("$(basename "$scenario")")
  fi
  echo
done

if [ "${#failed[@]}" -eq 0 ]; then
  printf '\033[32me2e: %d scenarios passed\033[0m\n' "$passed"
  exit 0
fi
printf '\033[31me2e: %d passed, %d failed\033[0m\n' "$passed" "${#failed[@]}"
for name in "${failed[@]}"; do
  printf '  %s\n' "$name"
done
exit 1
