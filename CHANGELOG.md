# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Optional worktree dispatch.** Moving a card into `in-progress` now asks
  whether to start an agent, and how isolated it should be: in the project, in a
  git worktree on the feature's own branch, or in a worktree with a pull request
  at the end. Declining is the default. `hvb run start --worktree` / `--pr` do
  the same from the shell.
- **Worktrees through Herdr's own support.** A worktree run uses
  `herdr worktree create`, so the checkout appears as a linked workspace grouped
  beside the project rather than as a directory nobody can see. The branch name
  follows VirtualBoard's convention, `feature/<ID>/<slug>`.
- **Pull requests.** A successful worktree run pushes its branch and opens a PR
  with the feature's acceptance criteria and the agent's commits in the body,
  then writes the link into the spec's Links section through `vb`. GitHub uses
  the `gh` CLI's existing credentials; Forgejo and Gitea use their REST API with
  a token from `forge.token` or `$HVB_FORGE_TOKEN`.
- **`hvb run cleanup`** removes a finished run's checkout, refusing while work is
  uncommitted unless forced.

### Fixed

- **A dispatched agent lands in its own workspace.** A plain run used to be
  split into whatever tab the board itself was running in, so dispatching from a
  board opened in another repository's workspace put the agent there. It now
  opens a workspace of its own, labelled like its pane (`ftr-0007 · qa`), filed
  in the same sidebar group as the workspace already hosting the project.
  Nothing is grouped if the project's workspace is not: an invented group name
  would add a sidebar folder nobody made.
- **A long prompt reaches the agent.** The task used to be passed to
  `bora agent prompt` in argv, where a ~11 KB prompt arrived as a collapsed
  paste marker — `[Paste #1, +222 lines]` — with the body lost, leaving an agent
  with no task. hvb now writes the prompt to a file beside the run store and
  submits a short pointer at it. A dispatch whose prompt cannot be written is
  refused rather than started blind.
- **The board answers while it reads.** Reloading ran on the event loop, so a
  board whose sources take tens of seconds — one measured read of the owner's
  issue queue took 69 — stopped reading the keyboard and stopped repainting for
  as long as the read lasted, every refresh. The read now runs off the loop and
  arrives as just another event, the header says `reading…` while it is out, and
  a refresh that finds one already in flight is dropped rather than queued. A
  result that arrives after the board moved on is discarded, never applied late:
  applying it would visibly undo a move the user already watched succeed.
- **A focused column is visible when it is empty.** Focus was a bold weight on a
  title that was already coloured, and an empty column had no card to highlight,
  so moving onto one looked exactly like not moving. The focused column now
  carries a `▸` caret and an inverted title, and its empty placeholder carries
  the caret too. The caret is text, so it survives `NO_COLOR`.
- **The mouse works.** Clicking a card focuses it, clicking the focused card
  opens it, and the wheel moves the selection in the column under the pointer.
  Set `HVB_NO_MOUSE` to keep the terminal's own text selection instead; the
  error-notice screen never asks for reporting, since its only verb is "press a
  key" and its text is the thing you want to copy.
- **`Esc` no longer closes the board.** It sat on the same arm as `q` and quit
  with no confirmation, which turned every escape sequence the terminal splits
  after its `ESC` byte into a way to lose the session by accident — a risk mouse
  reporting multiplies, since a wheel notch emits a sequence. `q` and ctrl-C
  quit; `Esc` keeps being the cancel in every overlay, and the only cancel in
  the new-feature form, where `q` types the letter.

### Notes

- A pull request that cannot be opened — no token, no client for the forge, no
  commits, a dirty checkout — never fails the run. The outcome stands, the
  feature still moves, and the reason is recorded with a compare URL where one
  can be built.
- Nothing here is on by default. No branch is cut and no forge is contacted
  unless a dispatch asks for it.

## [0.1.0] — 2026-09-15

First release.

### Added

- **The board.** `hvb tui` renders a VirtualBoard workspace as five lifecycle columns — backlog,
  in-progress, blocked, review, done — with card detail, run history, and a single-column layout
  below 108 terminal columns. Every mutation goes through `vb`, so the board and the repository
  cannot disagree.
- **Dispatch.** Pressing `d` starts a VirtualBoard role agent in a visible Herdr pane: one stable
  `ftr-####` tab per feature, a pane-first launch, and a prompt composed from the role charter, the
  agent contract, the column's stage instruction, and the spec.
- **Role selection** from `.virtualboard/agents/`, inferred from a feature's labels and status, with
  a `role:` label or `--role` overriding.
- **Outcome routing.** An agent reports once with `hvb run done`; the column's success and failure
  routes move the feature, and a blocked report goes to `blocked` wherever the lifecycle allows it.
- **Reconciliation without a daemon.** Every read path settles active runs against live Herdr state:
  a closed pane or a harness that finished without reporting parks the run as `awaiting` for a human,
  without moving the feature.
- **The CLI:** `feature list|show|new|move|set|note|delete|validate`, `run
  start|list|show|done|comment|cancel|focus|log`, `role`, `harness`, `doctor`, `skill`, `version`.
  `--json` on everything, with errors in a stable envelope on stderr and stdout left empty.
- **The agent contract** in `skill/SKILL.md`, printed verbatim by `hvb skill` and embedded in every
  dispatch prompt.
- **Plugin packaging:** `herdr-plugin.toml` with build steps, an overlay pane, and an idempotent
  open-or-focus-or-toggle launcher action.
- **Configuration** at `~/.config/herdr-virtualboard/config.toml` and `.hvb.toml`, merged field by
  field, with routes validated against the lifecycle at load time.

- **A notice screen** for every startup failure. An overlay pane whose command exits immediately is
  invisible, so a missing workspace, a missing `vb`, or an unsupported Herdr draws an explanation and
  waits rather than flickering and vanishing.

### Notes

- Requires exactly Herdr **0.9.0** (socket protocol 22). The version gate is policy, not
  negotiation; only cleanup of panes hvb already owns is exempt.
- A freshly split pane is not an available shell until its shell reaches a prompt, so `agent start`
  is retried for up to ten seconds.
- A plugin pane inherits the focused pane's working directory, so the board opens on whatever
  project you are looking at.

[0.1.0]: https://github.com/virtualboard/herdr-virtualboard/releases/tag/v0.1.0
