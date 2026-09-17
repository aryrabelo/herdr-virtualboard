#!/usr/bin/env bash
# Idempotent launcher for the VirtualBoard overlay, used by the
# `open-virtualboard` action and by a herdr keybinding.
#
#   no board pane in this session      -> open the overlay, focused
#   a board pane exists, not focused   -> focus it
#   the focused pane IS the board      -> close it
#
# Herdr has no hide-without-close, and reopening is cheap because the TUI
# refetches from disk, so toggling closed is the honest way to make one key
# both show and dismiss the board.
#
# Any failure degrades to OPEN. A launcher that does nothing because it could
# not parse a pane list is worse than one that opens a second board.
set -uo pipefail

herdr_bin="${HERDR_BIN_PATH:-bora}"

open_pane() {
  exec "$herdr_bin" plugin pane open \
    --plugin herdr-virtualboard \
    --entrypoint board \
    --placement overlay \
    --focus
}

# The TUI renames its own pane to "VirtualBoard · <project>", so match both the
# manifest title and that runtime label.
decision="OPEN"
if command -v python3 >/dev/null 2>&1; then
  panes="$("$herdr_bin" pane list 2>/dev/null || true)"
  if [ -n "$panes" ]; then
    decision="$(printf '%s' "$panes" | python3 -c '
import json, sys
try:
    data = json.load(sys.stdin)
except Exception:
    print("OPEN"); sys.exit(0)
result = data.get("result", data)
panes = result.get("panes", []) if isinstance(result, dict) else []
board = None
for pane in panes:
    name = pane.get("label") or ""
    if name == "VirtualBoard" or name.startswith("VirtualBoard · "):
        board = pane
        break
if not board:
    print("OPEN"); sys.exit(0)
pane_id = board.get("pane_id") or ""
if not pane_id:
    print("OPEN"); sys.exit(0)
print(("CLOSE " if board.get("focused") else "FOCUS ") + str(pane_id))
' 2>/dev/null || echo OPEN)"
  fi
fi

case "$decision" in
  "FOCUS "*) exec "$herdr_bin" plugin pane focus "${decision#FOCUS }" ;;
  "CLOSE "*) exec "$herdr_bin" pane close "${decision#CLOSE }" ;;
  *)         open_pane ;;
esac
