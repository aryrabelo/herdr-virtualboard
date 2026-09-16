#!/usr/bin/env bash
# The --json surface: every command emits parseable JSON on stdout, and errors
# go to stderr in a stable envelope with stdout left empty.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
trap teardown EXIT

echo "08-json-contract: machine-readable output"
setup
add_spec "$ROOT" backlog FTR-0001 "Something"

for command in "feature list" "run list --all" "role list" "harness list" "version" "doctor"; do
  # shellcheck disable=SC2086
  if hvb --json $command 2>/dev/null | python3 -c 'import json,sys; json.load(sys.stdin)' 2>/dev/null; then
    pass "hvb --json $command emits valid JSON"
  else
    fail "hvb --json $command emits valid JSON"
  fi
done

stdout_file="$(mktemp)"
stderr_file="$(mktemp)"
hvb --json feature show FTR-9999 >"$stdout_file" 2>"$stderr_file" || true

assert_equals "$(wc -c <"$stdout_file" | tr -d ' ')" "0" "a --json failure leaves stdout empty"
envelope="$(cat "$stderr_file")"
assert_contains "$envelope" '"error"' "the failure envelope is on stderr"
assert_contains "$envelope" '"code": 2' "the envelope carries the exit code"
assert_contains "$envelope" '"kind"' "the envelope classifies the failure"
if printf '%s' "$envelope" | python3 -c 'import json,sys; json.load(sys.stdin)' 2>/dev/null; then
  pass "the failure envelope is valid JSON"
else
  fail "the failure envelope is valid JSON"
fi
rm -f "$stdout_file" "$stderr_file"

finish
