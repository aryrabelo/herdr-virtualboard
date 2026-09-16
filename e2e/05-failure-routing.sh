#!/usr/bin/env bash
# Where a feature goes when an agent reports failure or says it is blocked.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
trap teardown EXIT

echo "05-failure-routing: failure and blocked outcomes move the feature"
setup
add_spec "$ROOT" in-progress FTR-0001 "Failing work"
add_spec "$ROOT" in-progress FTR-0002 "Blocked work"
add_spec "$ROOT" review      FTR-0003 "Rejected work"

start_run() {
  hvb --json run start "$1" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])'
}

# in-progress + failure -> blocked
run_id="$(start_run FTR-0001)"
out="$(hvb run done "$run_id" --outcome failure --note "cannot be done as specified" 2>&1)"
assert_contains "$out" "blocked" "failure from in-progress routes to blocked"

# in-progress + blocked -> blocked, whatever the column's failure route says
run_id="$(start_run FTR-0002)"
out="$(hvb run done "$run_id" --outcome blocked --note "waiting on an API key" 2>&1)"
assert_contains "$out" "blocked" "a blocked report routes to blocked"

# review + failure -> in-progress, because review cannot reach blocked
run_id="$(start_run FTR-0003)"
out="$(hvb run done "$run_id" --outcome failure --note "criteria not met" 2>&1)"
assert_contains "$out" "in-progress" "failure from review sends the work back"
assert_not_contains "$out" "blocked" "review never routes to blocked"

out="$(hvb feature list)"
assert_contains "$out" "BLOCKED" "the board now has a blocked column"

finish
