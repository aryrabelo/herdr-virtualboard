#!/usr/bin/env bash
# Local-development install: build hvb, link this checkout as a herdr plugin,
# and optionally add the keybinding.
#
# This is NOT what `herdr plugin install netors/herdr-virtualboard` runs — that
# path uses only the [[build]] steps in herdr-plugin.toml. Use this script when
# you are working on the plugin itself and want herdr to run your checkout.
#
#   ./scripts/install.sh              build, link, and install the CLI
#   ./scripts/install.sh --keybinding also add prefix+shift+v to herdr's config
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
herdr_bin="${HERDR_BIN_PATH:-herdr}"

add_keybinding=false
for arg in "$@"; do
  case "$arg" in
    --keybinding) add_keybinding=true ;;
    -h|--help)
      sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "install.sh: unknown argument: $arg" >&2
      exit 2
      ;;
  esac
done

command -v "$herdr_bin" >/dev/null 2>&1 || {
  echo "install.sh: herdr not found (set HERDR_BIN_PATH or install herdr)" >&2
  exit 1
}

echo "==> building"
"$script_dir/build.sh"

echo "==> installing the CLI"
"$script_dir/install-cli.sh"

echo "==> linking the plugin"
# Relinking an already-linked checkout is not an error worth stopping for.
"$herdr_bin" plugin link "$repo_root" || echo "install.sh: plugin link reported an error (already linked?)" >&2

if [ "$add_keybinding" = true ]; then
  config="${XDG_CONFIG_HOME:-$HOME/.config}/herdr/config.toml"
  mkdir -p "$(dirname "$config")"
  if grep -q "open-virtualboard" "$config" 2>/dev/null; then
    echo "install.sh: the keybinding is already in $config"
  else
    # prefix+shift+v, deliberately not prefix+v: lowercase single letters are
    # where herdr keeps its own pane-focus bindings.
    cat >>"$config" <<'TOML'

[[keys.command]]
key = "prefix+shift+v"
type = "shell"
command = "herdr plugin action invoke open-virtualboard --plugin herdr-virtualboard"
description = "open the VirtualBoard kanban (overlay)"
TOML
    echo "install.sh: added prefix+shift+v to $config"
    "$herdr_bin" server reload-config >/dev/null 2>&1 || \
      echo "install.sh: run \`herdr server reload-config\` to pick up the keybinding" >&2
  fi
fi

echo
echo "Installed. Open the board with:"
echo "  $herdr_bin plugin action invoke open-virtualboard --plugin herdr-virtualboard"
