#!/usr/bin/env bash
# Dispatching an agent: the pane-first launch sequence and the run record.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
trap teardown EXIT

echo "03-dispatch: pane-first launch into a herdr pane"
setup
add_spec "$ROOT" in-progress FTR-0001 "Add retry" '[backend]'

out="$(hvb --json run start FTR-0001 2>&1)"
assert_contains "$out" '"feature_id": "FTR-0001"' "the run names its feature"
assert_contains "$out" '"role": "backend_dev"'    "the role is inferred from the backend label"
assert_contains "$out" '"state": "running"'       "the run is running"
assert_contains "$out" '"pane_id": "w1:p3"'       "the run records its pane"

argv="$(herdr_argv)"
# The launch order is the contract: gate, then create the tab, then split a
# child with the run's cwd and environment, then start the agent in that child.
assert_contains "$argv" "status"      "the version gate ran before anything else"
assert_contains "$argv" "tab create"  "a feature tab was created"
assert_contains "$argv" "--label ftr-0001" "the tab is named after the feature"
assert_contains "$argv" "pane split w1:p2" "the run pane was split from the anchor"
assert_contains "$argv" "--env HVB_FEATURE_ID=FTR-0001" "the feature id reached the pane"
assert_contains "$argv" "--env HVB_RUN_ID=" "the run id reached the pane"
assert_contains "$argv" "--env HVB_ROLE=backend_dev" "the role reached the pane"
assert_contains "$argv" "--env HVB_ON_SUCCESS=review" "the success route reached the pane"
assert_contains "$argv" "agent start" "the agent was started"
assert_contains "$argv" "--kind claude --pane w1:p3" "the agent started in the split child, not the anchor"
assert_contains "$argv" "agent prompt" "the prompt was submitted after the agent came up"

# The anchor is closed after a successful launch, leaving one pane per run.
assert_contains "$argv" "pane close w1:p2" "the anchor pane was closed"

out="$(hvb run list)"
assert_contains "$out" "FTR-0001" "the run shows in the active list"

out="$(hvb --json run list)"
assert_contains "$out" '"state": "running"' "the run is listed as running"

# An explicit role overrides the label-derived suggestion.
add_spec "$ROOT" in-progress FTR-0002 "Other work" '[backend]'
out="$(hvb --json run start FTR-0002 --role qa 2>&1)"
assert_contains "$out" '"role": "qa"' "--role overrides the suggestion"

assert_exit 64 "an unknown harness exits 64" -- hvb run start FTR-0001 --harness notaharness

finish
