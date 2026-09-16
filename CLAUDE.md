# CLAUDE.md

Read [`AGENTS.md`](AGENTS.md) first — it holds the rules for changing this repository, and they
override anything you would otherwise assume.

The three that catch people out:

1. **Never write a feature spec directly.** Every mutation goes through `internal/vb`.
2. **Never take a Herdr command or JSON shape from memory.** Verify it against the installed binary
   and pin the argv in a test (`docs/herdr.md`).
3. **Never let a test touch a real Herdr session.** `HERDR_BIN_PATH` beats `PATH`; set it to a stub
   (`docs/testing.md`).

Before pushing: `make gates`.
