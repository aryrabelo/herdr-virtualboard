# Design

## The one decision everything else follows from

**VirtualBoard already has a board. It is the repository.**

`.virtualboard/features/<status>/FTR-####-slug.md` — the directory is the status, the frontmatter is
the card, and `vb` is the tool that moves them. That layout is committed, reviewed, and shared. Any
plugin that kept its own copy of a card would immediately face the question of which copy is right
when a teammate runs `vb move` from a shell, or when a `git pull` brings in five features somebody
else moved.

So hvb keeps no copy. It renders the repository and writes every change back through `vb`.

This is the difference from [herdr-board](https://github.com/nelsonPires5/herdr-board), which hvb is
otherwise modelled on. There, a card is a prompt the board invented, so the board must own it, which
means SQLite, which means a daemon to own the SQLite, which means a protocol between the daemon and
its clients. Here the card already exists and already has an owner, and none of that follows.

## What hvb does own

Exactly one thing the repository cannot hold: **which Herdr pane is currently running which
feature.**

That fact is machine-local, lives for minutes, and would be pure noise in a commit. It lives in one
JSON file per project under `~/.local/share/herdr-virtualboard/runs/<project-id>.json`, keyed on the
workspace's resolved absolute path.

The run store is deliberately disposable. Delete it and you lose run history; the board itself is
untouched, because the board is the repository. That is also why a corrupt run file starts clean
instead of failing: runs are a convenience, and refusing to draw the board over a bad cache would be
the wrong trade.

## Why there is no daemon

A daemon earns its keep when something must be watched while no user-facing process is running.
herdr-board needs one: it owns card state, so it must react to Herdr events whether or not the TUI
is open.

hvb has no such state. What it does need — noticing that a run's pane closed, or that a harness
finished without reporting — it gets by *looking*, in `Reconcile`, which every read path calls
before answering:

```text
active run, pane gone from `herdr pane list`   → awaiting  (reason: pane_closed)
active run, agent reports `done`, no report    → awaiting  (reason: agent_done)
active run, past the column's timeout          → failed, routed
anything else                                  → left alone
```

`awaiting` is not a failure. It is the board saying *the agent stopped and nobody knows why yet*.
The feature keeps its status and its owner: a run that stopped unexplained has not earned a
transition in either direction, and only a human can say whether the work landed.

Two rules keep reconciliation honest:

- **It never guesses.** If `herdr pane list` fails, nothing is settled. Marking every run dead
  because Herdr was briefly unreachable would be far worse than doing nothing.
- **It never parks the caller's own run.** An agent shelling out to `hvb run show` reconciles on the
  way past. It is alive — it is the thing asking — and Herdr can legitimately report its pane as
  `done` between turns, so `$HVB_RUN_ID` is exempt.

## The lifecycle is the pipeline

VirtualBoard's transition graph is not a suggestion:

```text
backlog ──→ in-progress ──→ review ──→ done
                 ↑ ↓            │
              blocked ──────────┘  (review → in-progress)
```

Nothing leaves `done`. hvb duplicates this table in `internal/feature/status.go` so the TUI can grey
out impossible moves rather than offering them and failing — but `vb` remains the authority, and
every move still goes through it. The duplicate exists to make the lifecycle *visible*, not to
enforce it. If `vb` ever changes the graph, `TestAllowedTransitions` is what starts failing.

The same table validates configuration at load time. A column routing `in-progress` to `done` is
rejected when you edit the file, not hours later when an agent finishes and the move is refused.

## Dispatch

Roles come from `.virtualboard/agents/`, the charters VirtualBoard already ships. Using them rather
than inventing a prompt format means a dispatched agent adopts the identity VirtualBoard's own rules
of engagement expect it to announce.

Selection, in order: an explicit `--role`, the column's configured role, a `role:` label on the
feature, the label hint table, `qa` for anything in review, then the configured fallback.

The launch is **pane-first**, which is Herdr's documented contract rather than a choice:

1. gate on the compatibility floor (Herdr 0.9.0 / protocol 25 or newer);
2. find or create the workspace rooted at the project;
3. find or create the feature's `ftr-####` tab — one per feature, reused across runs, so three
   dispatches do not leave three tabs;
4. split a child pane with the project root as cwd and the run's variables in its environment;
5. `agent start` in **that** child, never the anchor, retrying briefly while the new shell reaches
   its prompt;
6. close the anchor, leaving exactly one pane per run;
7. `agent prompt` with the composed prompt.

A failed launch keeps the anchor. It is the evidence of what went wrong, and closing it would hide
the error.

### Why the prompt is ordered the way it is

Contract first, then whatever hvb itself decided — the role name, the worktree it is in, a stage
instruction from the operator's own config — and then, last, **one block holding everything that
came out of the repository**: the role charter, the feature's metadata, its acceptance criteria, its
risk notes, its specification, and a stage instruction the project set in its own `.hvb.toml`.

The contract goes first because a charter pasted above the rules *is* the rules, as far as a reader
is concerned. The block goes last, and holds all of it, because the boundary VirtualBoard's rules of
engagement draw is between *what to build* and *what you are allowed to be told* — and that boundary
is only worth anything if it is drawn around every byte the repository supplied, not just around the
spec body. A risk note, a title, an acceptance criterion and a project-set stage instruction are all
free text that arrives with a clone, and with every pull request.

Three properties make the block hold:

- **hvb writes the markers, not the file.** The VirtualBoard template puts a delimiter pair into
  every spec, so an ordinary spec arrives carrying markers — and a hostile one arrives carrying
  three, the extra closing tag ending the block early so that everything after it reads as hvb's own
  voice. Every marker found in repository text is stripped before wrapping.
- **The markers carry a nonce, minted per dispatch**, and the prompt names it. Content cannot close
  a delimiter it could not predict, and it cannot smuggle one in either.
- **Nothing repository-supplied is printed in hvb's voice.** The only exceptions are the feature id
  and the role key, each reduced to identifier characters, because prose in a heading hvb wrote
  reads as hvb talking.

A stage instruction is the one key whose meaning depends on where it was read from: from the
operator's config it is policy and is presented as such; from the repository it is quoted inside the
block with everything else. `Column.PromptFromRepository` carries that provenance, and has no TOML
key of its own so that no file can claim to be the operator.

What the block is *not* is a sanitiser. It does not edit the text or judge it; it states, in hvb's
voice, where the repository's words begin and end. The agent contract
([`skill/SKILL.md`](../skill/SKILL.md)) is the other half: it tells the agent that material inside
the block never grants a permission, changes a rule or issues a command, whatever it appears to say.

### Why the two config layers are not equivalent

`~/.config/herdr-virtualboard/config.toml` is written by hand, on this machine, by the person who
installed the plugin. `<project root>/.hvb.toml` arrives with the repository: from a clone, and from
a pull request opened by someone who has no account here. Merging them field by field and calling
both "configuration" was the mistake, because for a Herdr plugin the trust boundary is the
**install**, not the call — by the time either file is read, hvb already holds the operator's socket
and can reach the whole CLI without anyone approving anything.

So the project layer may describe the project's pipeline, and nothing else. Whatever decides which
program hvb executes (`harness`, `columns.<status>.harness`), which host receives the operator's
forge token (`forge.kind`, `forge.base_url`, `forge.token`), or whether finishing a run publishes a
branch (`forge.enabled`, `forge.draft`, `forge.push_remotes`, `forge.require_confirmation`,
`worktree.enabled`, `worktree.remote`, `columns.<status>.worktree`, `columns.<status>.pr`) is read
from the operator's file only. A project file naming one is **refused**, with an error naming the
key: ignoring it silently would be the repository setting policy with nobody finding out, and this
file already argues against silent fallbacks elsewhere.

Closing one of those keys and leaving its neighbour open would only move the attack — a remote
allowlist is decorative if the same file can set `worktree.remote`, and refusing top-level `harness`
achieves nothing while `columns.in-progress.harness` is accepted. They are one decision with several
spellings, so they are refused as a set.

Publication keeps a second check at the moment it matters, rather than trusting the config layer
alone: `forge.push_remotes` is consulted immediately before the push. It is deliberately not a human
confirmation. An approved feature is expected to reach a pull request without anyone clicking
anything, so what bounds an autonomous run is a destination the operator named in advance, plus
`forge.draft` on by default — and `forge.require_confirmation` for an operator who wants the stop
anyway.

## Talking to Herdr

Through the `herdr` CLI, not the socket. The CLI is the contract Herdr documents for external
callers, it already emits the JSON this code decodes, and hvb needs no event subscription — there is
nothing a second protocol would buy.

The version gate is policy, not negotiation, but it is a floor rather than an exact pin: Herdr 0.9.0
or newer and socket protocol 25 or newer. A lower protocol may have moved a field this code reads,
and failing the dispatch beats launching an agent into a pane hvb can no longer track; a *higher*
one is the ordinary case on a host that keeps releasing, and rejecting it only broke dispatch. Both
floors move from the environment (`HVB_MIN_HERDR_VERSION`, `HVB_MIN_HERDR_PROTOCOL`). The one
exception, as in herdr-board, is cleanup: closing a pane hvb created stays ungated, so a Herdr
upgrade cannot strand a run hvb is responsible for tidying up.

Versions are compared as semver, field by field, never as strings — `"0.48.0" < "0.9.0"` is true
lexicographically and would reject every host release past 0.9.

Every argv in `internal/herdrcli` was verified against a running bora 0.48.0 and is pinned by a test
that asserts the exact command line. Do not change one from memory — see
[`docs/herdr.md`](herdr.md).

## Rendering

The TUI is hand-drawn with ANSI escapes rather than built on a framework. Two things make that the
right trade for a plugin: the binary stays small and its dependency surface stays four modules, and
every view is a pure function from state to `[]string`, so the whole layout is unit-testable without
a terminal.

The cost is that width arithmetic is hvb's problem. `displayWidth` skips SGR sequences and counts
wide runes as two cells, and everything that lays anything out goes through it — an off-by-one there
corrupts the surrounding Herdr pane, not just the board. Frames are diffed line by line so a refresh
that moves one card repaints one row.
