# Fork divergences

This is `aryrabelo/herdr-virtualboard`, a fork of
[`virtualboard/herdr-virtualboard`](https://github.com/virtualboard/herdr-virtualboard) adapted to
run on [`bora`](https://github.com/aryrabelo/bora), a fork of Herdr.

Every deliberate difference from upstream is listed here, with why it exists, where it lives, and how
to put it back if an upstream sync rewrites that hunk. Nothing else should differ. If you find a
difference that is not in this table, it is drift — either delete it or add a row.

Measured against upstream at `0d4bef4` ("docs: re-shoot the board on a demo project").

## The host this fork targets

| | value | how to re-measure |
|---|---|---|
| binary | `bora` | `which bora` |
| version | `0.48.0` | `bora status` → `server.version` |
| socket protocol | `25` | `bora status` → `client.protocol` |
| config dir | `~/.config/bora/` | `dirname "$(bora status \| sed -n 's/^  socket: *//p')"` |
| socket file | `herdr.sock` — **still that name** | same command, without `dirname` |

`HERDR_NAMESPACE` moves the config subdirectory, `HERDR_BIN_PATH` and `HERDR_ENV=1` are still the
variables the host injects into plugin panes, and the upstream names are still used on the wire. Only
the executable name and the config directory changed.

---

## 1. The compatibility gate is a floor, not an exact pin

**Files:** `internal/herdrcli/client.go` (`DefaultMinVersion`, `DefaultMinProtocol`, `EnvMinVersion`,
`EnvMinProtocol`, `Floor`, `ResolvedFloor`, `parseVersion`, `version.less`, `Gate`),
`internal/cli/version.go`, `internal/herdrcli/client_test.go`.

Upstream had:

```go
const (
	SupportedVersion  = "0.9.0"
	SupportedProtocol = 22
)
...
if status.ServerVersion != SupportedVersion { ... }
if status.ClientProtocol != SupportedProtocol { ... }
```

Two equality checks. On bora 0.48.0 that is `✗ herdr unsupported herdr version: need herdr 0.9.0,
found 0.48.0` — the plugin refuses to dispatch at all.

**Why the change is a policy fix and not a version bump.** Bumping the two constants to `0.48.0`/`25`
would work today and break on the host's next release, which is what happened here in the first
place. The author's intent — fail before launching an agent into a pane hvb cannot track — is kept;
what changes is *exactly this build* becoming *this build or newer*. A newer protocol is the ordinary
case on a host that keeps releasing; rejecting it only ever broke dispatch.

**The trap this fork exists to fix.** Version strings must never be compared as strings:

```
"0.48.0" < "0.9.0"   // true — '4' sorts before '9'
```

A string floor therefore rejects every host release past 0.9. `parseVersion` splits into numeric
`major`/`minor`/`patch` and `version.less` orders field by field.
`TestVersionOrdersFieldWiseNotLexicographically` pins the case both ways, in the minor field
(`0.48.0` vs `0.9.0`, `0.10.0` vs `0.9.0`) and in the patch field (`0.48.12` vs `0.48.9`), so the test
cannot pass today and lie at the next release. The protocol stays an `int` end to end.

**Operator escape hatch.** `HVB_MIN_HERDR_VERSION` and `HVB_MIN_HERDR_PROTOCOL` move either floor
without recompiling. A malformed value is an error, not a silent fallback — an operator who exported
the variable is entitled to know it did not take. Running against upstream Herdr 0.9.0 (protocol 22)
therefore only needs `HVB_MIN_HERDR_PROTOCOL=22`.

**Reapply after an upstream rewrite:** find the two equality comparisons in `Client.Gate`, replace
with `found.less(required)` and `status.ClientProtocol < floor.Protocol`, and keep `ResolvedFloor`
ahead of the `Status` call so a misconfigured floor fails before a socket round trip.

## 2. The binary fallback is `bora`

**File:** `internal/herdrcli/client.go` — `const Binary = "bora"` (upstream: `"herdr"`).

`HERDR_BIN_PATH` keeps precedence, so a pane the host spawned still uses the exact binary that
spawned it. Only the PATH fallback changed, for the case where hvb is run by hand outside a pane.

**Reapply:** one line. `TestBinaryFallsBackToBora` fails if it reverts.

## 3. `HERDR_BIN_PATH` is validated before it is executed

**File:** `internal/herdrcli/client.go` — `TrustedBinPath`, used by `New()`.

Upstream: `New()` returned `&Client{Bin: os.Getenv("HERDR_BIN_PATH")}` and executed that value
verbatim on every CLI call. hvb inherits its environment from a pane a dispatched agent also writes
to, and a bare name or relative path resolves against `PATH` or the process cwd — during a dispatch,
a repository an agent has just been writing files into. Dropping a `bora` there was enough to be run.

The value is now accepted only when it is an absolute path to a regular file with an execute bit;
anything else is ignored and the client falls back to `Binary` on `PATH`. Rejection is deliberately
silent, unlike a malformed version floor: that variable is operator intent, this one is host-injected,
and the safe response to a value that does not look host-injected is to ignore it.

This is finding VB-09 of the security audit run alongside this adaptation.

**Reapply:** re-wrap the `os.Getenv(EnvBinPath)` in `New()` with `TrustedBinPath`.
`TestBinPathRejectsAnythingNotAnAbsoluteExecutable` fails if it reverts.

## 4. Scripts call `bora` and ask the host where its config lives

**Files:** `scripts/install.sh`, `scripts/open-board.sh`.

- `${HERDR_BIN_PATH:-herdr}` → `${HERDR_BIN_PATH:-bora}` in both.
- `install.sh --keybinding` wrote to `${XDG_CONFIG_HOME:-$HOME/.config}/herdr/config.toml`, which on
  this host is a file nothing reads. It now parses the socket path out of `bora status` and takes its
  dirname, which is correct for upstream, for this fork, and for a `HERDR_NAMESPACE` override alike —
  the running server is the only thing that actually knows. It falls back to `~/.config/bora` with a
  warning when no server is running.
- The keybinding heredoc is no longer quoted, so the emitted `command =` uses `$herdr_bin` instead of
  a hardcoded `herdr`.

**Reapply:** the config-dir block is the only non-mechanical part; the rest is the binary name.

## 5. Fixtures report the host's real numbers

**Files:** `internal/herdrcli/client_test.go` (`supportedStatus`), `internal/dispatch/self_test.go`,
`e2e/lib.sh`.

The stub `status` output said `0.9.0`/`22`; it now says `0.48.0`/`25`, copied from a live `bora
status`. Without this the gate tests and `TestBlockedStartupParksTheTaskInsteadOfFailing` fail against
the new protocol floor. `e2e/07-doctor.sh` deliberately still stubs an *old* host (`0.8.0`/`19`) and
still asserts the report names `0.9.0` — that scenario needed no change, which is the evidence that
the floor still rejects what it should.

The fixture socket path stays `/tmp/herdr.sock`. The socket file really is called that.

## 6. Documentation states the floor and the host name

**Files:** `README.md`, `AGENTS.md`, `herdr-plugin.toml`, `docs/herdr.md`, `docs/design.md`,
`docs/configuration.md`, `docs/testing.md`, `docs/index.html`.

Statements of the form "exactly Herdr 0.9.0, socket protocol 22" are now the floor, and the five
copy-pasteable `herdr …` command lines are `bora …`. Two substantive notes were added rather than
renamed:

- `docs/herdr.md` records that `bora <group> <verb> --help` prints the *subcommand's* help, where
  upstream Herdr 0.9.0 printed the top-level help. `AGENTS.md`'s live-verification rule was updated to
  use it, because that is now the cheapest way to read a verb's flags.
- `herdr-plugin.toml` records that `min_herdr_version = "0.9.0"` is **left alone**: the host compares
  it as real semver (`bora:src/app/api/plugins/manifest.rs:239`, `required > current` over
  `crate::update::Version`) against `build_info::BASE_VERSION` = `0.48.0`, so the install-time floor
  already admits this host. Nothing in the manifest needed to change for the plugin to install.

## What was deliberately NOT changed

| Thing | Why |
|---|---|
| `herdr.sock` in every path | the socket file is named that on bora too |
| `internal/herdrcli` package name, `herdrcli.Error`, `ErrIncompatible`, `InHerdr`, `HERDR_BIN_PATH`, `HERDR_ENV`, `HERDR_PANE_ID` | the host still uses the upstream names on the wire and in the environment; renaming them would break the contract, not adapt to it |
| `"herdr"` as the `kind` in hvb's own JSON error envelopes (`internal/cli/app.go:261,269`) | it is hvb's own output contract — `errorKind` classifies a failure, it does not name a binary. `e2e/08-json-contract.sh:29` asserts the `kind` key is present; nothing asserts the value, so renaming it would be an unforced change to hvb's output with no adaptation gained |
| `herdr` as the `hvb doctor` check label | user-facing name for the host; changing it would churn `e2e/07-doctor.sh` for nothing |
| `~/.config/herdr-virtualboard/`, `~/.local/share/herdr-virtualboard/`, `.herdr-virtualboard-managed` | the plugin's *own* config, data and marker paths, named after the plugin id, not after the host |
| `min_herdr_version = "0.9.0"` | measured: the host's install gate compares it as semver, so it already passes — see §6 |
| every argv in `internal/herdrcli` | verified verb by verb and flag by flag against `bora <group> <verb> --help` on 0.48.0; **zero argv changes were needed**, including `--kind`'s 23 harnesses, which match `bora agent start --help` exactly |
| repository, org, and site URLs | this fork tracks upstream; it does not replace it |

## Re-verifying the whole adaptation

```bash
gofmt -l internal/herdrcli internal/cli/version.go
go build ./...
go test ./internal/herdrcli/ -count=1
./e2e/07-doctor.sh
cd /path/to/a/vb/workspace && hvb doctor    # six greens against a live host
```
