#!/usr/bin/env bash
# Copy the plugin-built `hvb` out of herdr's managed checkout into a directory
# on the user's PATH, so a dispatched agent can call `hvb run done` without
# knowing where herdr keeps its plugins.
#
# Two rules make this safe to run on every plugin update:
#
#   1. It never replaces a file it does not own. Ownership is recorded in a
#      marker file holding the SHA-256 of the binary this script installed; a
#      destination that exists without a matching marker is someone else's
#      `hvb` and is left alone.
#   2. Every placement is atomic — a hard link on first install, a rename on
#      update — so a concurrent `hvb` invocation never sees a partial binary.
#
# HVB_CLI_INSTALL_DIR overrides the destination (default: ~/.local/bin).
set -euo pipefail

if [ "$#" -ne 0 ]; then
  echo "install-cli.sh: no arguments expected; set HVB_CLI_INSTALL_DIR to change the destination" >&2
  exit 2
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
source_bin="$repo_root/bin/hvb"

if [ "${HVB_CLI_INSTALL_DIR+x}" = x ]; then
  [ -n "$HVB_CLI_INSTALL_DIR" ] || {
    echo "install-cli.sh: HVB_CLI_INSTALL_DIR must not be empty" >&2
    exit 2
  }
  install_dir="$HVB_CLI_INSTALL_DIR"
else
  [ -n "${HOME:-}" ] || {
    echo "install-cli.sh: HOME must be set when HVB_CLI_INSTALL_DIR is not given" >&2
    exit 2
  }
  install_dir="$HOME/.local/bin"
fi

case "$install_dir" in
  /*) ;;
  *)
    echo "install-cli.sh: the install directory must be an absolute path: $install_dir" >&2
    exit 2
    ;;
esac

[ -x "$source_bin" ] || {
  echo "install-cli.sh: no executable at $source_bin; run scripts/build.sh first" >&2
  exit 1
}

sha256_file() {
  local path="$1" output checksum
  if command -v sha256sum >/dev/null 2>&1; then
    output="$(sha256sum <"$path")" || return 1
  elif command -v shasum >/dev/null 2>&1; then
    output="$(shasum -a 256 <"$path")" || return 1
  else
    echo "install-cli.sh: sha256sum or shasum is required" >&2
    return 1
  fi
  checksum="${output%% *}"
  [[ "$checksum" =~ ^[0-9a-f]{64}$ ]] || {
    echo "install-cli.sh: could not compute a checksum for $path" >&2
    return 1
  }
  printf '%s\n' "$checksum"
}

if [ -e "$install_dir" ] && [ ! -d "$install_dir" ]; then
  echo "install-cli.sh: the install path exists but is not a directory: $install_dir" >&2
  exit 1
fi
mkdir -p -- "$install_dir"

destination="$install_dir/hvb"
marker="$install_dir/.herdr-virtualboard-managed"
marker_prefix="herdr-virtualboard install-cli.sh managed hvb sha256:"

# Never read the marker through a symlink or from a special file.
if { [ -e "$marker" ] || [ -L "$marker" ]; } && { [ ! -f "$marker" ] || [ -L "$marker" ]; }; then
  echo "install-cli.sh: the ownership marker is not a regular file: $marker" >&2
  exit 1
fi

managed=false
managed_checksum=""
if [ -f "$marker" ]; then
  marker_value="$(<"$marker")"
  if [[ "$marker_value" == "${marker_prefix}"* ]]; then
    candidate="${marker_value#"$marker_prefix"}"
    if [[ "$candidate" =~ ^[0-9a-f]{64}$ ]]; then
      managed=true
      managed_checksum="$candidate"
    fi
  fi
fi

if { [ -e "$destination" ] || [ -L "$destination" ]; } && [ "$managed" != true ]; then
  echo "install-cli.sh: refusing to overwrite an hvb this script does not own: $destination" >&2
  echo "install-cli.sh: move it aside, or set HVB_CLI_INSTALL_DIR to another absolute directory" >&2
  exit 1
fi
if [ "$managed" = true ]; then
  if [ ! -f "$destination" ] || [ -L "$destination" ]; then
    echo "install-cli.sh: the managed destination is not a regular file; refusing to replace it: $destination" >&2
    exit 1
  fi
  destination_checksum="$(sha256_file "$destination")"
  if [ "$destination_checksum" != "$managed_checksum" ]; then
    echo "install-cli.sh: $destination has changed since this script installed it; refusing to replace it" >&2
    exit 1
  fi
fi

temporary="$(mktemp "$install_dir/.hvb.XXXXXX")"
marker_temporary="$(mktemp "$install_dir/.herdr-virtualboard-managed.XXXXXX")"
cleanup() { rm -f -- "$temporary" "$marker_temporary"; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

cp -p -- "$source_bin" "$temporary"
chmod 0755 "$temporary"
installed_checksum="$(sha256_file "$temporary")"
marker_value="${marker_prefix}${installed_checksum}"
printf '%s\n' "$marker_value" >"$marker_temporary"
chmod 0644 "$marker_temporary"

if [ "$managed" = true ]; then
  mv -f -- "$temporary" "$destination"
else
  # A hard link is an atomic no-clobber creation, so a destination that
  # appeared between the check above and here is not overwritten.
  if ! ln -- "$temporary" "$destination"; then
    echo "install-cli.sh: something created $destination during the install; refusing to overwrite it" >&2
    exit 1
  fi
  rm -f -- "$temporary"
fi
mv -f -- "$marker_temporary" "$marker"
trap - EXIT HUP INT TERM

actual="$(sha256_file "$destination")"
[ "$actual" = "$installed_checksum" ] || {
  echo "install-cli.sh: the installed binary does not match what was staged" >&2
  exit 1
}

echo "install-cli.sh: installed $destination"
case ":${PATH}:" in
  *":$install_dir:"*) ;;
  *) echo "install-cli.sh: note — $install_dir is not on your PATH" >&2 ;;
esac
