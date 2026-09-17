# Configuration

Everything here is optional. hvb works with no configuration at all; the defaults are the
VirtualBoard lifecycle with human gates everywhere and Claude Code as the harness.

## Files

Two layers, merged **field by field**, later winning:

| Path | Scope |
|---|---|
| `$HVB_CONFIG`, else `$XDG_CONFIG_HOME/herdr-virtualboard/config.toml`, else `~/.config/herdr-virtualboard/config.toml` | your defaults across every project |
| `<project root>/.hvb.toml` | this project |

Field-by-field means a project file naming one column does not erase the rest of the pipeline, and a
project file setting `harness` does not reset your global `role`.

The project file sits **beside** `.virtualboard/`, not inside it, because `vb init --update`
re-applies the upstream template over that directory and would overwrite anything hvb left there.

An unknown key is an error rather than a silent no-op, so a typo is reported when you save the file.

## Top level

```toml
harness          = "claude"        # default agent kind; see `hvb harness list`
role             = "fullstack_dev" # fallback when labels and status suggest nothing
owner            = ""              # handle hvb claims features under; see below
lock_ttl_minutes = 60              # vb lock TTL a dispatch takes; 0 disables locking
start_timeout    = "90s"           # bounds `herdr agent start`
prompt_timeout   = "10m"           # bounds a waiting `herdr agent prompt`
placement        = "overlay"       # where `hvb tui` opens as a plugin pane
```

`owner` resolves at use time: the configured value, then `$HVB_OWNER`, then `$USER`, then the
hostname, then `hvb`. It is written to the feature's `owner` frontmatter and taken as the `vb` lock
holder, so it is how a teammate or another board sees that a feature is taken.

`lock_ttl_minutes = 0` turns locking off entirely, which is the right setting for a board only one
person drives.

## Columns

One table per lifecycle status. Every key is optional.

```toml
[columns.in-progress]
auto       = false        # dispatch an agent as soon as a feature lands here
role       = "backend_dev"# override the suggested role for this column
harness    = "claude"     # override the harness for this column
prompt     = "Implement this feature end to end."
on_success = "review"     # where a successful run moves the feature
on_failure = "blocked"    # where a failed run moves it
timeout    = "45m"        # bound a run here; 0 or unset means no bound
```

`prompt` is the column's stage instruction, prepended to the dispatch prompt the way a herdr-board
column's system prompt is. It is where you say *what this stage is for* — build it, check it, harden
it — as opposed to what the feature is.

Without `auto`, the column is a human gate: work stops there until somebody moves the card on.

`on_success` and `on_failure` **must be legal VirtualBoard transitions from that column**, and are
checked when the file loads. A column routing `in-progress` to `done` is rejected as you save it,
not hours later when an agent finishes.

### The defaults

```toml
[columns.backlog]                  # a gate: nothing happens until you move a card out
[columns.in-progress]
on_success = "review"
on_failure = "blocked"
prompt     = "Implement this feature end to end. When the acceptance criteria are met, report success; if you are blocked by something you cannot resolve, report failure with the reason."
[columns.blocked]                  # a gate
[columns.review]
role       = "qa"
on_success = "done"
on_failure = "in-progress"
prompt     = "Review this feature against its acceptance criteria. Verify the tests actually exercise the behaviour. Report success only if every criterion is met; otherwise report failure and say exactly what is missing."
[columns.done]                     # terminal
```

### Turning the pipeline on

The defaults never dispatch by themselves. To make moving a card into `in-progress` start an agent,
and a successful review close the feature:

```toml
[columns.in-progress]
auto = true

[columns.review]
auto = true
```

## Environment

| Variable | Meaning |
|---|---|
| `HVB_CONFIG` | override the global config path |
| `HVB_DATA_DIR` | where run files live (default `~/.local/share/herdr-virtualboard`) |
| `HVB_PROJECT_ROOT` | project root, as if `--root` had been passed |
| `HVB_OWNER` | the handle hvb claims features under |
| `HVB_VB_BIN` | the `vb` executable to use |
| `HERDR_BIN_PATH` | the host executable to use (default `bora`); the host injects this into panes it manages |
| `HVB_MIN_HERDR_VERSION` | lower or raise the host version floor (default `0.9.0`) |
| `HVB_MIN_HERDR_PROTOCOL` | lower or raise the socket protocol floor (default `25`; use `22` for upstream Herdr 0.9.0) |
| `HVB_CLI_INSTALL_DIR` | where `scripts/install-cli.sh` puts `hvb` (default `~/.local/bin`) |
| `NO_COLOR` | draw the board without colour |

### Set inside a dispatched agent's pane

These are the run's contract, documented in [`skill/SKILL.md`](../skill/SKILL.md). An agent reads
them; nothing else should set them.

| Variable | Meaning |
|---|---|
| `HVB_FEATURE_ID` | the feature the agent owns |
| `HVB_RUN_ID` | this dispatch |
| `HVB_ROLE` | the charter it adopted |
| `HVB_STATUS` | the status it was dispatched from |
| `HVB_PROJECT_ROOT` | the repository root, also the pane's cwd |
| `VIRTUALBOARD_ROOT` | the same, for VirtualBoard tooling |
| `HVB_ON_SUCCESS` / `HVB_ON_FAILURE` | where a report will move the feature; absent when the column routes nowhere |
