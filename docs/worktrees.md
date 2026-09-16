# Worktrees and pull requests

Optional, per dispatch. Nothing here happens unless you ask for it: no branch is
cut, no directory is created and no forge is contacted by a board that nobody
told to.

## Why it is a question, not a setting

Moving a card into `in-progress` is the moment work starts. It is also the only
moment when the person moving it has the context to answer "should something
else do this, and how separate should it be?" — they know whether the feature is
a five-minute edit or a day's work, and whether they want to watch it happen in
their own checkout.

So the board asks once, there, with declining as the default. A setting buried in
a config file would be answered once and then be wrong for most cards.

## What a worktree run is

```text
project/                        your checkout, untouched
  .virtualboard/features/…      the board, still the source of truth

~/.herdr/worktrees/project/feature-ftr-0007-add-retry/
                                the agent's checkout, on its own branch
```

hvb calls `herdr worktree create`, which cuts the branch *and* opens the checkout
as a **linked workspace** — grouped beside the project in the sidebar, not a
directory nobody can see. The agent's pane lives in that workspace with the
checkout as its working directory.

The branch is `feature/<ID>/<slug>`, the convention VirtualBoard's own
`scripts/worktree-setup.sh` uses, so a branch hvb cuts is indistinguishable from
one made by hand.

A second dispatch of the same feature **reuses** the existing worktree rather
than failing or making a second one: a redispatch is a retry, and the work should
accumulate on one branch.

## What the agent is told

The dispatch prompt gains a section, and `skill/SKILL.md` gains two rules. Both
exist because an agent that does not know it is on a branch fails in one specific
way — it leaves work uncommitted, and there is then nothing to open a pull
request from.

- `HVB_WORKTREE`, `HVB_BRANCH` and `HVB_BASE_BRANCH` are in its environment.
- It must commit. Uncommitted work is discarded when the checkout is cleaned up.
- It must not switch branches, rebase, or touch the main checkout.
- It must not push and must not open the pull request. hvb does both, so the
  branch, the board and the spec stay consistent instead of racing.

## The pull request

On `success`, and only on success, hvb:

1. checks the checkout is clean — uncommitted work means the branch does not
   contain everything the agent did, and a PR from it would be misleading;
2. checks the branch is ahead of its base — an agent can legitimately decide
   nothing needed changing;
3. pushes the branch;
4. opens the pull request;
5. writes the URL into the spec's **Links** section through `vb`, so the link
   lives in the repository and not only in hvb's local run store;
6. lets the ordinary column routing move the feature.

The description carries the feature's acceptance criteria — with the boxes as
they stand *now*, re-read from the spec, so a criterion the agent ticked shows
ticked — plus its commit subjects and the role that produced them.

### Forges

| Forge | How | Needs |
|---|---|---|
| GitHub | the `gh` CLI | nothing — it uses the credentials you already have |
| Forgejo, Gitea | REST API | a token in `forge.token` or `$HVB_FORGE_TOKEN` |
| GitLab, anything else | — | nothing; you get a compare URL |

Using `gh` rather than the GitHub API is deliberate. `gh` already holds your
credentials, in your keyring, scoped and refreshed; asking you to mint a second
token for hvb to store would be worse in every way.

Forgejo and Gitea share an API and have **no `draft` field on creation** — their
own web UI expresses a draft as a `WIP:` title prefix, so that is what hvb sends.

A self-hosted forge on a neutral hostname (`code.internal`) is genuinely
unknowable from the remote URL. hvb says `unknown` rather than guessing; set
`forge.kind` to tell it.

### Failure is not failure

**Nothing in this path can fail a run.** The agent has already done the work and
committed it; losing that outcome because a token was missing would be absurd.
Every one of these is a recorded reason plus a compare URL, with the run still
`succeeded` and the feature still moved:

| Situation | What you get |
|---|---|
| no token for Forgejo/Gitea | compare URL, reason names the missing token |
| `gh` not installed or not logged in | compare URL, reason says `gh auth login` |
| no client for the forge | compare URL, reason names the host |
| the token was rejected | compare URL, reason names the status |
| branch has no commits | no push, reason says so |
| checkout is dirty | no push, reason names the directory |
| a PR is already open | treated as success, its URL recovered |

## Cleanup

```bash
hvb run cleanup [RUN-ID] [--force]
```

Removes the checkout and closes its workspace. It refuses while the checkout has
uncommitted changes, because that work exists nowhere else — separateness is the
whole point of a worktree. `--force` discards it.

Cleanup is never automatic. A finished branch is often exactly what you want to
look at next.

## Testing it locally

The whole loop runs with no network, no credentials and no forge account, by
using a bare repository on disk as the remote:

```bash
mkdir -p ~/tmp/wt-test && cd ~/tmp/wt-test && git init -q -b main && vb init
git add -A && git commit -qm init
git init -q --bare ~/tmp/wt-origin.git
git remote add origin ~/tmp/wt-origin.git && git push -q -u origin main

hvb feature new "Add retry to the uploader" -l backend
hvb feature move FTR-0001 in-progress
hvb run start FTR-0001 --worktree --pr
```

The branch is cut, the agent runs in its own workspace, and on success the push
is real — `git -C ~/tmp/wt-origin.git branch` will show it. There is no forge
behind a local path, so the pull request degrades to a reason, which is the
point: you see the whole path including how it behaves when a forge is not
reachable.

`e2e/09-worktree-pr.sh` automates exactly this.

## The startup dialog

The first dispatch into a new worktree usually stops before it starts:

```text
hvb: the claude agent is waiting on its own startup dialog.
     Answer it in the pane (`hvb run focus <run-id>`); hvb submits the task
     as soon as the agent is ready. It never answers that dialog for you.
```

A worktree is a directory the harness has never seen, so Claude Code asks
whether you trust it. Herdr reports this as `agent_not_ready`, which sounds like
a failure and is not: the agent is running, its name is valid, and only a human
answering stands between it and working.

hvb therefore treats it as a launched agent, parks the task on the run, and
submits it the moment the agent goes idle — the next board refresh, or the next
`hvb run list`. You do nothing but answer the dialog.

**hvb never answers it for you.** Whether to trust a directory is a security
decision, and a board that clicks "yes, I trust this folder" on your behalf
would be making it without being asked.

Once answered, the harness remembers that path, so subsequent dispatches into
the same worktree start straight into the task.
