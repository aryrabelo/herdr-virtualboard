#!/usr/bin/env bash
# Reading the board: discovery, listing, filtering, and detail.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
trap teardown EXIT

echo "01-board-basics: reading a VirtualBoard workspace"
setup
add_spec "$ROOT" backlog     FTR-0001 "First feature"  '[backend]'  P1
add_spec "$ROOT" in-progress FTR-0002 "Second feature" '[frontend]' P0
add_spec "$ROOT" review      FTR-0003 "Third feature"  '[]'         P2
add_spec "$ROOT" done        FTR-0004 "Fourth feature" '[]'         P3

out="$(hvb feature list)"
assert_contains "$out" "FTR-0001" "list shows a backlog feature"
assert_contains "$out" "FTR-0004" "list shows a done feature"
assert_contains "$out" "BACKLOG"  "list groups by status"
assert_contains "$out" "IN PROGRESS" "list names the in-progress column"

out="$(hvb feature list --status review)"
assert_contains     "$out" "FTR-0003" "--status keeps the matching feature"
assert_not_contains "$out" "FTR-0001" "--status drops the rest"

out="$(hvb feature list --label backend)"
assert_contains     "$out" "FTR-0001" "--label keeps the labelled feature"
assert_not_contains "$out" "FTR-0002" "--label drops the rest"

out="$(hvb --json feature list)"
assert_contains "$out" '"total": 4'   "--json reports the total"
assert_contains "$out" '"id": "FTR-0001"' "--json carries feature ids"

out="$(hvb feature show FTR-0001)"
assert_contains "$out" "First feature"       "show renders the title"
assert_contains "$out" "Acceptance criteria" "show renders the criteria"
assert_contains "$out" "Summary for First"   "show renders the summary"

# Discovery must work from a nested directory, the way it does when a user runs
# hvb from wherever they happen to be in the repository.
mkdir -p "$ROOT/src/deep"
out="$(cd "$ROOT/src/deep" && "$HVB" feature list)"
assert_contains "$out" "FTR-0001" "the workspace is discovered from a subdirectory"

# A feature id is not case-sensitive: nobody wants to shout at a CLI.
out="$(hvb feature show ftr-0001)"
assert_contains "$out" "FTR-0001" "feature ids resolve case-insensitively"

assert_exit 2 "a missing feature exits 2 (not found)" -- hvb feature show FTR-9999

finish
