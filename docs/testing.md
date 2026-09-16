# Testing

```bash
make gates
```

That is the whole contract. CI runs exactly that target, so a green local run means a green
pipeline, and there is no second list of steps to keep in sync.

`gates` is `fmt-check`, `vet`, `test-race`, and `e2e`.

## Unit tests

`make test` / `make test-race`. They cover the parts hvb owns:

| Package | What is pinned |
|---|---|
| `feature` | the whole transition graph, frontmatter parsing, section extraction, acceptance criteria |
| `workspace` | discovery, symlink-stable identity, directory-over-frontmatter status, broken specs surfacing |
| `vb` | the argv sent to `vb` and the mapping from its exit codes to typed errors |
| `herdrcli` | the exact argv sent to `herdr`, response decoding, the version gate |
| `runs` | idempotent completion, concurrent writes, pruning, Herdr's agent-name grammar |
| `roles` | charter loading, role suggestion precedence |
| `config` | field-wise merging, and rejecting routes the lifecycle forbids |
| `dispatch` | prompt composition, outcome routing, reconciliation |
| `tui` | rendering geometry, navigation, every overlay, and that colour costs no width |
| `cli` | exit-code mapping, the JSON error envelope, command-tree completeness |

Two of these are worth knowing about because they exist to catch a specific mistake:

- **`TestAllowedTransitions`** asserts the entire lifecycle graph. `internal/feature/status.go`
  duplicates the table from vb-cli so the TUI can grey out impossible moves; if vb ever changes the
  graph, this is what should start failing.
- **`TestEmbeddedSkillMatchesThePublishedOne`** fails when `skill/SKILL.md` and
  `internal/dispatch/skill.md` drift. They are two files only because `go:embed` cannot reach out of
  its package directory. Run `make sync-skill`.

### Testing against stub binaries

`internal/vb` and `internal/herdrcli` are tested against shell stubs that record their argv. Testing
against the real tools would make the suite depend on a VirtualBoard install and on Herdr's release
cadence; testing against a stub pins **the argv hvb actually emits**, which is the part hvb owns.

## End-to-end

`make e2e`, or one scenario directly:

```bash
./e2e/03-dispatch.sh
```

Each scenario runs the real `hvb` binary against a real temporary VirtualBoard workspace, with stub
`vb` and `herdr` executables. The `vb` stub actually moves spec files between directories, so a
scenario can assert that the board changed rather than only that a command was called.

| Scenario | Covers |
|---|---|
| `01-board-basics` | discovery from a subdirectory, listing, filters, detail, `--json` |
| `02-lifecycle` | every legal and illegal transition, including that nothing leaves `done` |
| `03-dispatch` | the pane-first launch sequence, the injected environment, anchor cleanup |
| `04-agent-contract` | what a dispatched agent can do with only its run environment |
| `05-failure-routing` | where failure and blocked outcomes send a feature from each column |
| `06-reconciliation` | a vanished pane and a silent harness both parking as `awaiting` |
| `07-doctor` | the environment report, and refusing an unsupported Herdr |
| `08-json-contract` | valid JSON on stdout, the error envelope on stderr, stdout empty on failure |

### Why the stubs are not optional

The suite must never touch the developer's Herdr session. Note that **`HERDR_BIN_PATH` takes
precedence over `PATH`** — which is correct, because a plugin running inside Herdr should use the
binary that spawned it — so putting a stub on `PATH` is not enough. `e2e/lib.sh` sets
`HERDR_BIN_PATH` and `HVB_VB_BIN` explicitly. Removing either makes the suite create real panes and
real workspaces in whatever session you happen to be running.

This was not hypothetical: the first version of the suite did exactly that.

## Testing against a real Herdr

Sometimes you have to, because only a live server catches a wrong field name. Do it in a scratch
directory and clean up after yourself:

```bash
vb init
hvb doctor                    # prove the gate passes
hvb run start FTR-0001 --harness claude
herdr tab list --workspace <ws>
herdr workspace close <ws>    # close what you created, and only that
```

When you learn something from a live server, pin it in `internal/herdrcli/client_test.go` and record
it in [`docs/herdr.md`](herdr.md) so the next person does not have to rediscover it.
