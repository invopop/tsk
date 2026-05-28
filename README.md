# tsk

Multi-repo task workspaces backed by git worktrees.

If your work routinely touches a handful of repos at once — services and the
libraries they share — `tsk` automates the bootstrap: one task directory, one
worktree per repo, all on a fresh branch off `origin/main`.

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

The base branch is hardcoded to `origin/main`.

## `tsk close` is paranoid by default

Closing a task removes each worktree and deletes the task directory. Before doing
that, `close` refuses to touch a worktree if either:

- the working tree is dirty, or
- the branch was never pushed, or has unpushed commits ahead of `origin/<branch>`.

This is the whole point: it is easy to forget that a worktree had local-only
work. `--force` is the explicit escape hatch for the cases where you really do
want to discard.

## Commands

```
tsk create [<ref>] <slug>          Create a task directory in cwd
tsk add <repo-path> [...] [-b]     Add worktrees to the current task
tsk status                         git status summary across all worktrees
tsk rm [-f] <repo-path>            Remove one worktree from the current task
tsk close [-f] <task-path>         Decommission a task: clean worktrees + delete dir
```

`add`, `status` and `rm` walk up from cwd looking for `.tsk.yaml`, so they work
from any subdirectory inside a task. `close` takes the path explicitly so you
can run it from outside.

## License

Apache-2.0 — see [LICENSE](LICENSE).
