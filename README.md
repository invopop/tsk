# tsk

Multi-repo task workspaces backed by git worktrees.

If your work routinely touches a handful of repos at once — services and the
libraries they share — `tsk` automates the bootstrap: one task directory, one
worktree per repo, all on a fresh branch off each repo's default upstream.

## Install

```sh
go install github.com/invopop/tsk
```

## A 60-second tour

```sh
# 1. Create a task in the current directory.
tsk create app-489 multi-payment-methods
# → /path/to/cwd/app-489-multi-payment-methods

cd app-489-multi-payment-methods

# 2. Add as many repos as the task needs.
#    Each becomes a worktree on a new branch named after the slug.
tsk add ../../gobl.html ../../pdf.go ../../at-pt

# 3. Look across worktrees at any time.
tsk status
# gobl.html  [multi-payment-methods]  M 3   ↑2
# pdf.go     [multi-payment-methods]  clean
# at-pt      [multi-payment-methods]  clean

# 4. Drop a repo if it turns out you don't need it.
tsk rm pdf.go

# 5. When the work is shipped (and pushed!) close the task.
cd ..
tsk close app-489-multi-payment-methods
```

## Convention over configuration

There is no global config and no central tasks directory. A task is just a
directory containing a `.tsk.yaml` marker:

```yaml
ref: app-489
slug: multi-payment-methods
```

Run `tsk create` wherever you want the task to live. The task directory's name is
`<ref>-<slug>` (or just `<slug>` if you skip the ref). The branch created on each
source repo by `tsk add` defaults to `<slug>` — the ref is **not** part of the
branch name.

The base branch defaults to the first remote's default branch — e.g. on a
typical clone, `origin/main`, but `tsk` follows whatever the repo is actually
configured with (`upstream/master` works just the same). Override it with
`--base <remote>/<branch>` on `tsk add` (or `tsk create` when using `-a`):

```sh
# Base the new worktrees off origin/develop instead of the default.
tsk add ../../gobl.html --base origin/develop
```

The full `<remote>/<branch>` form is required so it's never ambiguous whether
you mean a local branch or a remote-tracking one.

When `--base` helps:

- **Stacking on an unreleased feature branch.** Your task depends on a
  colleague's change that is approved but not yet merged to `main`. Branching
  off their feature branch keeps your diff focused on your own work instead
  of dragging in theirs, and avoids the "merge their branch into mine, then
  rebase later" dance.
- **Long-lived integration branches.** When several tasks land into a shared
  `develop` (or similar) before promotion, base new worktrees there so each
  task starts from the state the integration branch is actually in.

## `tsk close` is paranoid by default

Closing a task removes each worktree and deletes the task directory. Before doing
that, `close` refuses to touch a worktree if either:

- the working tree is dirty, or
- the branch was never pushed, or has unpushed commits ahead of its upstream.

This is the whole point: it is easy to forget that a worktree had local-only
work. `--force` is the explicit escape hatch for the cases where you really do
want to discard.

## Commands

```
tsk create [<ref>] <slug> [--base <remote>/<branch>] [-a <repo>...]
                                   Create a task directory in cwd
tsk add <repo-path> [...] [-b <branch>] [--base <remote>/<branch>]
                                   Add worktrees to the current task
tsk status                         git status summary across all worktrees
tsk rm [-f] <repo-path>            Remove one worktree from the current task
tsk close [-f] <task-path>         Decommission a task: clean worktrees + delete dir
```

`add`, `status` and `rm` walk up from cwd looking for `.tsk.yaml`, so they work
from any subdirectory inside a task. `close` takes the path explicitly so you
can run it from outside.

## License

Apache-2.0 — see [LICENSE](LICENSE).
