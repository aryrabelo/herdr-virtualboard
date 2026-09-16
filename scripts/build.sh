#!/usr/bin/env bash
# Build the `hvb` binary. This is the plugin's first [[build]] step, so herdr
# runs it at install time; scripts/install.sh calls it too.
#
# Idempotent: Go's build cache makes a rebuild with no source changes a no-op.
# Output: bin/hvb, relative to the repo root, which is what the [[panes]] entry
# in herdr-plugin.toml executes.
set -euo pipefail

# Resolve the repo root from this script's own location, because herdr invokes
# build steps from a working directory the plugin does not control.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
cd "$repo_root"

if ! command -v go >/dev/null 2>&1; then
  # Homebrew and the official tarball are the two common installs that land
  # outside a login shell's PATH when herdr spawns the build.
  for candidate in /usr/local/go/bin /opt/homebrew/bin "$HOME/go/bin"; do
    if [ -x "$candidate/go" ]; then
      export PATH="$candidate:$PATH"
      break
    fi
  done
fi
command -v go >/dev/null 2>&1 || {
  echo "build.sh: go not found (install the Go toolchain: https://go.dev/dl/)" >&2
  exit 1
}

version="$(cat "$repo_root/VERSION" 2>/dev/null || echo dev)"
mkdir -p "$repo_root/bin"

echo "build.sh: go build hvb ${version} (repo: $repo_root)"
go build -trimpath -ldflags "-s -w -X main.version=${version}" -o "$repo_root/bin/hvb" ./cmd/hvb

bin="$repo_root/bin/hvb"
if [ ! -x "$bin" ]; then
  echo "build.sh: expected binary not found at $bin" >&2
  exit 1
fi
echo "build.sh: ok -> $bin"
