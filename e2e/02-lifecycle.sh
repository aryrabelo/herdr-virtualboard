#!/usr/bin/env bash
# The VirtualBoard lifecycle: which moves hvb performs and which it refuses.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
trap teardown EXIT

echo "02-lifecycle: transitions are checked before vb is called"
setup
add_spec "$ROOT" backlog     FTR-0001 "Backlog item"
add_spec "$ROOT" in-progress FTR-0002 "Active item"
add_spec "$ROOT" review      FTR-0003 "Review item"
add_spec "$ROOT" done        FTR-0004 "Finished item"

out="$(hvb feature move FTR-0001 in-progress 2>&1)"
assert_contains "$out" "in-progress" "backlog → in-progress is performed"
assert_contains "$(vb_argv)" "move FTR-0001 in-progress" "vb performed the move"

# An illegal transition is refused by hvb itself, so the message names the
# lifecycle rather than surfacing an exit code from vb.
out="$(hvb feature move FTR-0002 done 2>&1 || true)"
assert_contains "$out" "does not allow" "in-progress → done is refused"
assert_contains "$out" "blocked, review" "the refusal lists the legal moves"
assert_exit 64 "an illegal move exits 64 (usage)" -- hvb feature move FTR-0002 done

# Nothing leaves done. This is the rule VirtualBoard is strictest about.
assert_exit 64 "done → in-progress is refused" -- hvb feature move FTR-0004 in-progress
assert_exit 64 "done → review is refused"      -- hvb feature move FTR-0004 review

# review → done and review → in-progress are both legal.
out="$(hvb feature move FTR-0003 done 2>&1)"
assert_contains "$out" "done" "review → done is performed"

assert_exit 64 "an unknown status exits 64" -- hvb feature move FTR-0001 archived

# Moving a feature to where it already is is a no-op the user should be told
# about, not a silent success.
out="$(hvb feature move FTR-0001 in-progress 2>&1 || true)"
assert_contains "$out" "already in" "a redundant move says so"

finish
