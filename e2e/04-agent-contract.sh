#!/usr/bin/env bash
# The contract a dispatched agent is held to: what it can read, what it reports,
# and what the board does with the report.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
trap teardown EXIT

echo "04-agent-contract: an agent works its feature and reports once"
setup
add_spec "$ROOT" in-progress FTR-0001 "Add retry" '[backend]'

run_id="$(hvb --json run start FTR-0001 | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
[ -n "$run_id" ] || { echo "dispatch produced no run id"; exit 1; }

# From here on, simulate the dispatched agent: it has the run environment and
# nothing else, and must be able to do its whole job through it.
export HVB_RUN_ID="$run_id"
export HVB_FEATURE_ID="FTR-0001"
export HVB_ROLE="backend_dev"

out="$(hvb feature show)"
assert_contains "$out" "FTR-0001"  "the agent reads its own feature with no arguments"
assert_contains "$out" "Add retry" "the spec title is present"

out="$(hvb run show)"
assert_contains "$out" "$run_id"      "the agent reads its own run with no arguments"
assert_contains "$out" "backend_dev"  "the run names the role"

hvb run comment "Found the existing retry helper." >/dev/null
out="$(hvb run show)"
assert_contains "$out" "Found the existing retry helper." "a comment is recorded"
assert_contains "$out" "backend_dev" "the comment is attributed to the role"

out="$(hvb skill)"
assert_contains "$out" "hvb run done"          "the contract documents the reporting call"
assert_contains "$out" "Never move the feature yourself" "the contract forbids self-moves"
assert_contains "$out" "HVB_FEATURE_ID"        "the contract documents the environment"

# The report is the one call that changes the board.
out="$(hvb run done --outcome success --note "criteria met" 2>&1)"
assert_contains "$out" "success" "the outcome is accepted"
assert_contains "$out" "review"  "success routes in-progress to review"
assert_contains "$(vb_argv)" "move FTR-0001 review" "vb performed the routed move"

out="$(hvb --json run show)"
assert_contains "$out" '"state": "succeeded"' "the run ended as succeeded"
assert_contains "$out" '"moved_to": "review"' "the run records where the feature went"

# Reporting twice must not rewrite the first outcome.
hvb run done --outcome failure --note "second thoughts" >/dev/null 2>&1 || true
out="$(hvb --json run show)"
assert_contains "$out" '"state": "succeeded"' "a second report does not overwrite the first"

assert_exit 64 "an unknown outcome exits 64" -- hvb run done --outcome maybe

finish
