# The Herdr contract

hvb drives Herdr through its CLI. This page records what was verified and, more importantly, **how
to verify it again** — because the installed binary is the authority and this page is only a cache.

> Never take a Herdr command, flag, or JSON shape from memory or from this page when you are about
> to depend on it in code. Read it live, then pin it in a test.

## Live sources of truth

| Command | What it gives you |
|---|---|
| `herdr <group>` (bare, e.g. `herdr pane`) | usage for every verb in that group, with its flags. This is the one that matters — `herdr pane split --help` prints the *top-level* help, not the subcommand's. |
| `herdr api schema --json` | the full socket API: every method, its parameters, every event, and the top-level `protocol` number |
| `herdr api snapshot` | live runtime state — what is actually open right now |
| `herdr status` | client and server versions, the protocol, the socket path |
| `herdr --skill` | Herdr's own agent-driving contract |

```bash
test "$(herdr --version)" = "herdr 0.9.0"
herdr api schema --json | python3 -c 'import json,sys; assert json.load(sys.stdin)["protocol"]==22'
```

## The compatibility gate

hvb supports **exactly Herdr 0.9.0, socket protocol 22**. `herdrcli.Gate` checks it before any
operation that creates layout or launches an agent. It is policy, not protocol negotiation.

The deliberate exception is cleanup and liveness for panes hvb already owns: `ClosePane` and pane
reads stay ungated, so a Herdr upgrade cannot leave hvb unable to tidy up a run it started.

## Verified argv

Every line below was run against Herdr 0.9.0 and is pinned by a test in
`internal/herdrcli/client_test.go`. The tests assert the exact string, so an edit made from memory
fails there rather than at dispatch time.

```text
herdr status
herdr workspace list
herdr workspace create --cwd PATH --label TEXT [--env K=V]... --no-focus
herdr tab list --workspace WS
herdr tab create --workspace WS --cwd PATH --label TEXT [--env K=V]... --no-focus
herdr tab rename TAB LABEL
herdr tab close TAB
herdr pane list [--workspace WS]
herdr pane get PANE
herdr pane split PANE --direction right --cwd PATH [--env K=V]... --no-focus
herdr pane rename PANE LABEL
herdr pane close PANE
herdr pane read PANE --source recent-unwrapped --lines N
herdr agent list
herdr agent get TARGET
herdr agent start NAME --kind KIND --pane PANE --timeout MS
herdr agent prompt TARGET TEXT [--wait] [--timeout MS]
herdr agent focus TARGET
herdr notification show TEXT
herdr plugin pane open --plugin ID --entrypoint ID --placement overlay --focus
herdr plugin pane focus PANE
```

`--env` arguments are emitted in sorted order so the argv is reproducible.

## Response shapes that are easy to get wrong

These were wrong in the first draft and only surfaced against a live server.

- **Tabs report `focused`, not `active`.**
- **Panes have no `title` field.** An unrenamed pane carries `terminal_title` and
  `terminal_title_stripped`; `label` appears only once something has renamed it. `Pane.Name()`
  prefers the label and falls back.
- **`cwd` is the kernel's resolved path.** Ask for `/tmp` on macOS and Herdr reports `/private/tmp`.
  Both sides are resolved before comparing, or a workspace that is right there is never found.
- **A newly split pane is not an available shell yet.** `agent start` refuses it with
  `agent_pane_busy` until its shell reaches an interactive prompt. hvb retries for up to ten seconds,
  which is safe only because hvb created the pane and nothing else can be using it.

## Lifecycle states

Herdr reports `idle`, `working`, `blocked`, `done`, `unknown` per pane, and only with the matching
harness integration installed (`herdr integration install claude`).

- `idle` and `done` both mean ready for input; the server distinguishes them by whether the
  completion has been seen.
- `blocked` means Herdr recognised an approval or question UI.
- **`unknown` is not completion.** It means an agent is present but Herdr cannot classify it.

Without the integration, none of `working` / `blocked` / `done` exist. Dispatch, `hvb run done`,
column timeouts, and pane-exit handling all still work; hvb simply cannot notice an agent that
finished without reporting until its pane closes.

## Rules hvb follows

From `herdr --skill`, and worth restating because they are easy to violate by accident:

- `agent start` requires an **existing** available shell pane and never creates or moves layout.
  Hence pane-first.
- A successful `agent prompt` proves submission, not that the agent started a turn. A timeout does
  not prove the prompt was never delivered — **never blindly resubmit one**.
- Parse ids out of JSON responses. Never derive them from ordering or from examples.
- Do not close workspaces, tabs, or panes hvb did not create.
- `--no-focus` for anything the user did not explicitly ask to be taken to.

## Plugin panes

A plugin pane opened by an action inherits **the focused pane's working directory**, not the plugin
root. That is what makes the board follow whatever project you are looking at, and it is why
`scripts/open-board.sh` passes no `--cwd`. Verified by reading `plugin_pane.pane.cwd` out of a live
`plugin action invoke`, which also returns the invocation `context` — `focused_pane_cwd`,
`workspace_cwd`, and the ids around them.

An overlay pane whose command exits immediately is **invisible**: it opens, the process ends, and
the user sees a flicker with no explanation. Anything that stops `hvb tui` from starting therefore
renders a notice screen and waits for a keypress rather than returning an error. See
`internal/tui/message.go`.

When reading a pane running a full-screen TUI, remember that `--lines N` returns the **last** N rows
of the viewport. A board's content is at the top, so `--lines 30` on a 54-row pane shows nothing but
blank rows. Ask for more rows than the pane has.
