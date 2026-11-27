---
name: ilot
description: "Run, stop, inspect and debug the apps of a workspace with the ilot CLI. Use it for any task about an app, a port, a log, a proxy URL, a stack, an ilot.yaml or ilot itself. Examples: start an API to test a change, read its logs, find its URL, check an ilot.yaml."
allowed-tools: "Bash(ilot ls:*), Bash(ilot apps:*), Bash(ilot logs:*), Bash(ilot env:*), Bash(ilot check:*), Bash(ilot start:*), Bash(ilot restart:*), Bash(ilot stop:*)"
---

# ilot

`ilot` runs the apps that the `ilot.yaml` files of the repos declare. It starts each app with its dependencies, gives it ports, puts it behind the ilot proxy and keeps its logs.

## The rule

`ilot` is yours to run. Run `ilot start`, `ilot restart`, `ilot stop`, `ilot logs`, `ilot ls`, `ilot apps`, `ilot env`, `ilot stack show`, `ilot doctor` and `ilot check` yourself. Do not ask the user to run them, and do not tell the user to "start the API": start it.

This skill pre-approves `ilot ls`, `ilot apps`, `ilot logs`, `ilot env`, `ilot check`, `ilot start`, `ilot restart` and `ilot stop`. Every other `ilot` command asks the user for permission first. These limits still apply:

- Before you run `ilot rm` or `ilot clean` on a stack that you did not start, ask the user. Both delete the stack's state, logs and containers.
- Before you stop an app of the `base` stack that you did not start, ask the user. Other stacks and the user's other sessions use them.
- Before you run `ilot daemon stop` or `ilot daemon restart`, ask the user. Both stop every app of every stack, the user's other sessions included.
- Never run `ilot daemon run`. `ilot` starts its daemon by itself, and the command is hidden from the help.

## Stacks

A stack is the set of apps that run for one branch of work.

- `ilot` names the stack of a linked worktree after its branch, in lowercase letters, digits and dashes: the branch `feature/retry-jobs` belongs to the stack `feature-retry-jobs`. If the `ilot.yaml` of the root repo has `stack.pattern`, the name is the first match of that pattern in the branch name. A worktree with a detached HEAD takes the name of its folder. `ilot stack show` prints the stack of the current folder.
- The main checkout of every repo is the `base` stack, on any branch.
- An app with `scope: base` in its `ilot.yaml` runs one time, in the `base` stack, for every stack.
- A workspace is a root repo and its member repos. The `ilot.yaml` of the root lists the members under `repos`, and the `ilot.yaml` of a member names the root with `member_of`. `ilot` finds the workspace from the repo of the current folder.
- The first `ilot start` in a worktree binds the worktree to its stack. After that, `git switch`, a rebase or a bisect does not move it to another stack. `ilot stack show` prints the stack of the current worktree and the checkout of each repo for it. `ilot stack bind <name>` puts the current worktree in another stack: ask the user first, because the apps of the old stack keep running.
- A stack takes each repo's worktree that is bound to it. If a repo has no such worktree, the stack takes that repo's main checkout.
- If a worktree does not start an app, the app comes from the `base` stack, and `ilot` starts it there if necessary. An app with `scope: stack` is the exception: an app that depends on it always gets the copy of its own stack.

Run every `ilot` command inside a repo of the workspace: outside one, `ilot` does not find the workspace and says so. `ilot start`, `ilot stop`, `ilot restart`, `ilot logs`, `ilot apps`, `ilot env` and `ilot stack` act on the stack of the folder that you are in. `ilot ls`, `ilot rm` and `ilot clean` act on every stack. From another folder, give `--workspace <name>`, and `--stack <name>` for a stack other than `base`.

## Commands

```bash
ilot ls                         # every app of every stack: status, ports, proxy URL; then the worktree of each stack other than base
ilot ls --json                  # the same as one JSON object, with the checkout of each repo per stack: use it to read the list from a script
ilot apps                       # every app and external service that the ilot.yaml files declare for this folder's stack, with the dependencies of each app
ilot apps --json                # the same as one JSON object
ilot start front                # build, then start the app after its dependencies, in this folder's stack; an app that runs stays as it is
ilot start                      # start every app that this folder's stack owns and that does not run
ilot start api --no-deps        # start only this app
ilot start --all                # start every app that a stack owns and that does not run, after a crash of the daemon
ilot stop api                   # stop the app in this folder's stack
ilot stop --all                 # stop every app that this folder's stack owns
ilot logs api                   # the last 100 lines of the app's log in this stack; a JSON line becomes one readable line
ilot logs --json api            # the app's own lines, as they are: use this to read logs
ilot logs -f -n 20 api          # the last 20 lines, then the new ones: it never ends, so run it in the background, never in a foreground command
ilot env api                    # the environment of the app, with where each value comes from
ilot check                      # check the ilot.yaml of this folder's repo, with the other repos' ilot.yaml
ilot check path/to/ilot.yaml     # check that file
ilot rm retry-jobs              # stop a stack and delete its state, its logs and its routes (ask first)
ilot clean                      # ilot rm on every stack whose bound worktrees are all deleted (ask first); a moved worktree keeps its stack
ilot clean --dry-run            # print what ilot clean would do, and change nothing: run it before you ask
ilot logs --workspace shop --stack retry-jobs api  # select a workspace and a stack from any folder
ilot stack show                 # the stack of this worktree, and the checkout of each repo for it
ilot stack bind retry-jobs      # put this worktree in the stack retry-jobs (ask first)
ilot restart api                # build the app again, then restart it, in this folder's stack; it must run already
ilot doctor                     # test what ilot needs on this machine; each line that needs a fix names the fix
ilot daemon status              # the version, the process ID and the proxy address of the ilot daemon
ilot daemon stop                # stop every app of every stack, then the daemon (ask first)
ilot daemon restart             # stop every app and the daemon, start a new daemon, then start the apps of this workspace again (ask first)
ilot --version                  # the version of ilot
ilot skill print                # print this skill
ilot skill install              # copy this skill to ~/.claude/skills/ilot/SKILL.md
```

`ilot start` leaves an app that runs as it is, unless its specification changed. After a code change, run `ilot restart <app>`: it builds the app again and restarts it. An app with `watch` in its `ilot.yaml` is rebuilt and restarted when its files change: you do not need `ilot start` after an edit. If its build fails, the old version keeps running, and the error is in `ilot logs <app>-watch`. For other apps, after a code change, run `ilot restart <app>`.

## Where things are

- Proxy URLs: `http://<app>.<stack>.<workspace>.localhost:4580`. `ilot ls` prints them. The page `http://localhost:4580` lists them all. The URL of an app that does not run answers 503 with the command that starts it.
- State: `~/.local/state/ilot/state.json`. Logs: `~/.local/state/ilot/workspaces/<workspace>/<stack>/logs/<app>.log`. What each command did, as JSON records: `~/.local/state/ilot/ilot.log`. Warnings and progress lines go to stderr, so the stdout of `ilot ls --json`, `ilot apps --json` and `ilot env` is data only.
- Apps: the `ilot.yaml` at the root of each repo. `ilot apps` lists their names, their repo and their dependencies. A checkout with no `ilot.yaml` reads the file of the main checkout of its repo.
- The global file: `~/.config/ilot/config.yaml`. It holds the address and the domain of the proxy (`proxy`), the port range (`ports`), the flags of the shell (`shell_env.args`), and, under `workspaces.<workspace>`, the path of a member repo that is cloned at another place (`repos.<name>`) and personal values (`env`, and `apps.<app>` for one app). Do not put secrets in a repo file: put them there.
- An app gets a fixed environment. The values of the shell files come first: `ilot` reads the whole environment of a login shell (`$SHELL -l`), and `ilot env` marks these values `(shell)`. A login shell reads the profile files, for example `~/.zprofile`, and not `~/.zshrc`: if an app does not find a tool, compare the `PATH` of `ilot env <app>` with the `PATH` of the terminal. The values of the `ilot.yaml` files and of the global file come on top. An `export`, an `unset` or a `.envrc` in a shell never reaches an app: put the value in the global file. An app gets its environment when it starts: after a change, run `ilot restart <app>`. If a command prints `warning: the ilot daemon has the version`, the daemon is older than `ilot`. Ask the user before you run `ilot daemon restart`.
- The README of ilot describes every key and template of `ilot.yaml`.

## When you edit an ilot.yaml

Run `ilot check` after every change to a `ilot.yaml`. It merges the file with the other repos' `ilot.yaml`, runs the config checks, and renders every app's command, env and mounts. It finds typos in fields and templates, and duplicate app names.

When you review a PR that changes a `ilot.yaml`, run `ilot check` on it too.

## When a start fails

1. Read the error that `ilot start` prints. It names the app and its log file.
2. Run `ilot logs --json <app>` and read the last lines.
3. Run `ilot env <app>` to see the ports and the values it got.
4. Run `ilot ls` to see which dependencies run and where.

`ilot start` checks the container images before any build. It pulls a missing image (`pulling <image>`), once for all apps that share it. A failed pull stops `ilot start` before the builds. The error is `pull <image>: <error>`, and it can end with the hint `log in to the registry, then run the command again: docker login <registry>`. The login needs the user. Tell the user, wait, then run the same `ilot start` again.

When an app of the start ends, it stays in its stack: `ilot ls` shows it `exited (<code>)` and its log stays. Fix the cause, then run the same `ilot start` again. It starts that app again, in the same stack, and every other app of the start that does not run. A dependency that runs gets no call and is never stopped.

When the timeout passes while the app still runs, the error ends with `it still starts: ilot keeps it`. The app stays. Do not run `ilot restart` for it: that restarts it from zero. Follow its log with `ilot logs -f <app>` until it is ready, and check with `ilot ls` that it still runs. Next time, use a longer `--timeout`. Ctrl-C keeps the app too.

`ilot start --all` does the same image check. A failed image fails only that app and the apps that depend on it.

A port that "is taken by another process" belongs to something outside `ilot`. Find it, and ask the user before you stop it.

An app that `ilot ls` marks `undeclared` is in no `ilot.yaml` of its stack any more. It keeps its ports. `ilot stop <app>` in its stack removes it.

## Traps

- A stack takes one worktree of a repo. A second worktree whose branch gives the same stack name gets an error that names the first. Give it another stack with `ilot stack bind <name>`.
- A container app declares its ports in `container.ports`, with the port inside the container. `ilot` publishes each one on `127.0.0.1` on a port of the stack, which `ilot ls` and `ilot env` show. A container reaches another app with `{{host "<app>"}}`, which gives it `host.docker.internal`.
- If you test a build of `ilot` with a separate `XDG_STATE_HOME`, also give it a separate `HOME` whose `~/.config/ilot/config.yaml` sets `proxy.listen` to a free address, for example `127.0.0.1:4590`. Without it, the scratch daemon cannot open the proxy port that the real daemon holds.
- If a command prints `warning: the proxy cannot listen`, another program has the proxy port. The apps run, but their URLs do not answer. Tell the user.
- The `run` command of an app must stay in the foreground. When its shell exits, the daemon kills what is left in its process group: a `run` that starts a server with `&`, or a tool that forks to the background, ends at once, and `ilot ls` shows it `exited`. Use `exec`, or the flag of the tool that keeps it in the foreground.
- A stop can take up to twice the stop timeout of 20 seconds: a `stop` command gets 20 seconds, then the app gets SIGTERM and 20 more seconds before SIGKILL. A slow `ilot stop` is not stuck.
- On Linux, a container app reaches only the apps that listen on every interface: `host.docker.internal` is the address of the Docker bridge there, not `127.0.0.1`. Docker Desktop on macOS has no such limit.
- A container app is ready at once with a bare `ready: {port: ...}`: Docker accepts the connection before the server inside listens. Give it `ready.path` or `ready.command`. `ilot check` warns about it.
- In zsh, a shell variable named `path` overwrites `PATH`. Name loop variables something else.
