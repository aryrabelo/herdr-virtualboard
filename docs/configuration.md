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
project file setting `role` does not reset your global `owner`.

The two layers are **not** equivalent. The global file is yours, written by hand on this machine. The
project file arrives with the repository — from a clone, and from a pull request opened by someone
with no account here — so it may not set what a repository has no business deciding.

The project file sits **beside** `.virtualboard/`, not inside it, because `vb init --update`
re-applies the upstream template over that directory and would overwrite anything hvb left there.

An unknown key is an error rather than a silent no-op, so a typo is reported when you save the file.

## Operator-only keys

These may only be set in the global config. A `.hvb.toml` that names one is **refused**, with an
error naming the key and the path of your own config — silence here would be the repository choosing
policy and nobody finding out.

| Key | Why it is yours |
|---|---|
| `harness`, `columns.<status>.harness` | it is the program hvb starts, with your credentials |
| `forge.kind`, `forge.base_url`, `forge.token` | together they decide which host receives an authenticated API call, so a repository able to set them could redirect your forge token to a host it chose |
| `forge.enabled`, `columns.<status>.pr` | they turn an ordinary dispatch into a push and a pull request |
| `forge.draft` | a draft is the safe default; a repository must not be able to clear it |
| `forge.push_remotes`, `forge.require_confirmation` | they *are* the publication policy |
| `worktree.enabled`, `columns.<status>.worktree` | they decide whether a dispatch gets a branch at all, which is what a pull request is opened from |
| `worktree.remote` | it names the destination a finished branch is published to |

A project file may still describe its own pipeline: `role`, `owner`, the timeouts, `placement`,
`worktree.branch`, `worktree.base`, and per column `auto`, `role`, `prompt`, `on_success`,
`on_failure` and `timeout`.

`columns.<status>.prompt` is accepted from a project file, but it is not treated as your policy. A
stage instruction hvb reads out of the repository is quoted to the agent as repository material,
inside the delimited block with the spec — see [`design.md`](design.md). The same key in your global
config is presented as policy, in hvb's own voice.

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

## The production line

`hvb queue` draws whatever line `[workflow] columns` declares. `hvb tui` never
does: it renders VirtualBoard spec markdown, and `vb` owns those five states.
A `.hvb.toml` naming a column `vb` does not know is refused on the spec board,
with `vb` named as the authority; the same file opens a twelve-column queue
board without complaint.

```toml
[workflow]
columns = ["intake", "triage", "planning", "building", "first-review",
           "fr-approved", "pr-processing", "pr-verification",
           "ready-to-review", "ready-to-merge", "done", "canceled"]
```

Without this key the queue board keeps the shape it always had: the five
VirtualBoard states plus a `canceled` tail that appears only when something is
in it.

Each declared column takes four more keys, on top of the pipeline keys above:

```toml
[columns."pr-verification"]
title = "PR Verification"      # the column heading; derived from the name if unset
short = "PV"                   # the breadcrumb abbreviation; derived if unset
when  = "hvb:state:open"       # the fact that puts a card here — see below
next  = ["building", "ready-to-review"]

[columns."ready-to-review"]
gate  = "human"                # nothing leaves without a person
quiet = true                   # a green pull request that has gone quiet lands here
next  = ["ready-to-merge", "building"]
```

`next` replaces VirtualBoard's transition table **on the queue board only**. It
is what the move picker offers and what `H`/`L` obey; a move the line does not
declare is refused naming the columns it does.

Twelve columns do not fit on a terminal, so the board draws a window of them
around the focused one and puts the whole line in a breadcrumb above, with `‹`
and `›` where it continues off screen. `--focus-column ready-to-review` opens
the board on one column in particular, which is how a `prefix+k` binding lands
straight on the one column only a human unblocks.

### Where a column lives

Six of the columns above are facts the forge can prove. hvb reads those from
GitHub on every refresh and **never writes them anywhere**: a card's `when`
label is minted by the source that measured it, and a fact outranks anything
hvb remembered.

| `when` label | means |
|---|---|
| `hvb:state:merged` | the pull request was merged |
| `hvb:state:canceled` | the pull request closed without merging |
| `hvb:state:open` | the pull request is open |
| `hvb:state:closed` | the issue is closed |

The other six — `triage`, `planning`, `first-review`, `fr-approved`,
`ready-to-review`, `ready-to-merge` — have no GitHub field at all, so hvb
remembers them itself, in one file per repository under
`$HVB_DATA_DIR/columns/`. Moving a card into one of those is recorded there and
nowhere else; moving it into a column a `when` label decides is refused, because
the next refresh would overrule the write.

`hvb state show --repo owner/name --json` prints what hvb remembers as a JSON
array, so `kit.py` and anything else can read a card's column without hvb
writing to GitHub to publish it. A card nobody has moved has no row: its column
is whatever the forge and the source say.

### The quiet timer

A pull request advances to the column declaring `quiet = true` only when **both**
facts hold: the forge said green (`hvb:check:green`, minted for an explicit
success and for nothing else), and its last *measured* activity is at least a
window old. Anything less holds the card in the open-pull-request column and
says which half is missing in the detail:

| what the card carries | what happens |
|---|---|
| green, quiet for ≥ the window | advances, `verde e quieto por <tempo> (timer <janela> do repo <repo>)` |
| green, quiet for less | holds, `quieto há <tempo>, faltam <resto> de <janela>` |
| no green — checks queued, expected, or none reported | holds, `sem check verde: o avanço exige sucesso medido, não ausência de vermelho` |
| `hvb:check:red` | holds, `check vermelho: retrocesso a building disponível` |
| no readable `hvb:activity:` stamp | holds, `sem atividade medida na PR` |

**Absence of a red check is not green.** A pull request nobody has tested never
advances on a timer, however long it sits: the window is there to skip the
second look at work CI already approved, not to approve it.

How long "quiet" is belongs to the repository:

```toml
[repos."aryrabelo/bugtoprompt"]
quiet_timer = "30m"
```

A declared window is between `10m` and `24h`; any other value is refused naming
both bounds. Below ten minutes the line advances a pull request whose checks are
still being queued — a run that has not started has no activity to measure, so
silence looks like calm. Above a day nobody is waiting for the board.

Zero is the one value outside those bounds the file accepts, because it is not
a window at all but the explicit off switch: `quiet_timer = "0s"` loads, and is
read exactly like declaring no key.

**A repository that declares no window never advances on its own.** Its cards
hold in the open-pull-request column with `timer não configurado para
<repo>` in the detail, because guessing a window here would move somebody's
pull request forward with nobody's say-so.

The countdown restarts on the last *measured* activity: the newest check
timestamp or the newest comment, whichever is later. `updatedAt` deliberately
does not count — a label change or a body edit bumps it without anyone touching
the pull request. A red check holds the card whatever the clock says.

Reading checks and comments costs seconds of GraphQL per refresh (measured
against gh 2.97.0: 7.9s for twenty pull requests), so hvb asks for them only
when a column declares `quiet = true`.

### Dispatching an issue card

`d` on a GitHub issue card hands it to `usina agente dispatch`, which cuts the
worktree, opens the workspace and mints the scoped token. It never goes through
the run store's own despatcher — that claims the card through `vb`, and `vb` has
never heard of an issue number. One despatcher per card.

It needs the **execution** repository, which is not the issue's own:

```
hvb queue --issues aryrabelo/ceo-bora --label project:bugtoprompt \
          --dispatch-repo aryrabelo/bugtoprompt
```

Without `--dispatch-repo`, pressing `d` on an issue card says so rather than
guessing which repository to branch from.

## Environment

| Variable | Meaning |
|---|---|
| `HVB_CONFIG` | override the global config path |
| `HVB_DATA_DIR` | where run files and the column store live (default `~/.local/share/herdr-virtualboard`) |
| `HVB_PROJECT_ROOT` | project root, as if `--root` had been passed |
| `HVB_OWNER` | the handle hvb claims features under |
| `HVB_VB_BIN` | the `vb` executable to use |
| `HERDR_BIN_PATH` | the host executable to use (default `bora`); the host injects this into panes it manages |
| `HVB_MIN_HERDR_VERSION` | lower or raise the host version floor (default `0.9.0`) |
| `HVB_MIN_HERDR_PROTOCOL` | lower or raise the socket protocol floor (default `25`; use `22` for upstream Herdr 0.9.0) |
| `HVB_FORGE_TOKEN` | the Forgejo/Gitea token, when you would rather not write it into a file |
| `HVB_TRUST_REPOSITORY` | set to `1`, `true`, `yes` or `on` to let hvb pass `--trust-repository` on worktree commands. Unset, hvb leaves the host's own repository-trust gate alone rather than answering it for you |
| `HVB_CLI_INSTALL_DIR` | where `scripts/install-cli.sh` puts `hvb` (default `~/.local/bin`) |
| `HVB_QUEUE_DISPATCH_REPO` | default for `hvb queue --dispatch-repo`: the execution repository `usina agente dispatch` branches from |
| `HVB_QUEUE_FOCUS_COLUMN` | default for `hvb queue --focus-column`: the column the board opens on |
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
