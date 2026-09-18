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

pane="$(run_pane)"
assert_contains "$out" "\"pane_id\": \"$pane\"" "the run records its pane"

argv="$(herdr_argv)"
# The launch order is the contract: gate, then open the run its own workspace
# with the run's cwd and environment, then start the agent in that workspace's
# root pane.
assert_contains "$argv" "status"           "the version gate ran before anything else"
assert_contains "$argv" "workspace create" "the run opened a workspace of its own"
assert_contains "$argv" "--label ftr-0001" "the workspace is named after the feature"
assert_contains "$argv" "--env HVB_FEATURE_ID=FTR-0001" "the feature id reached the pane"
assert_contains "$argv" "--env HVB_RUN_ID=" "the run id reached the pane"
assert_contains "$argv" "--env HVB_ROLE=backend_dev" "the role reached the pane"
assert_contains "$argv" "--env HVB_ON_SUCCESS=review" "the success route reached the pane"
assert_contains "$argv" "agent start" "the agent was started"
assert_contains "$argv" "--kind claude --pane $pane" "the agent started in the run workspace's root pane"
assert_contains "$argv" "agent prompt" "the prompt was submitted after the agent came up"

# A plain run is never split into whatever tab the board happens to be running
# in: that is the defect the owner watched, and the run's own workspace is the
# fix (internal/dispatch, createRunWorkspace). So there is no tab to cut in
# someone else's workspace, no anchor to split from, and none to close.
assert_not_contains "$argv" "tab create" "no tab was cut in another workspace"
assert_not_contains "$argv" "pane split" "the run was not split off the caller's pane"
assert_not_contains "$argv" "pane close" "there was no anchor pane to close"

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
