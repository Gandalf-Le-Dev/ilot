---
name: ilot
description: "Run, stop, inspect and debug the apps of a workspace with the ilot CLI. Use it for any task about an app, a port, a log, a proxy URL, a stack, an ilot.yaml or ilot itself. Examples: start an API to test a change, read its logs, find its URL, check an ilot.yaml."
allowed-tools: Bash(ilot:*)
---

# ilot

`ilot` runs the apps that the `ilot.yaml` files of the repos declare. It starts each app with its dependencies, gives it ports, puts it behind the ilot proxy and keeps its logs.

## The rule

`ilot` is yours to run. Run `ilot start`, `ilot stop`, `ilot logs`, `ilot ls`, `ilot apps`, `ilot env` and `ilot check` yourself. Do not ask the user to run them, and do not tell the user to "start the API": start it.

Every `ilot` command is pre-approved for you. These limits still apply:

- Before you run `ilot rm` or `ilot clean` on a stack that you did not start, ask the user. Both delete the stack's state, logs and containers.
- Before you stop a shared app (for example Temporal, auth-mock, restapi) or a `master` app that you did not start, ask the user. Other stacks and the user's other sessions use them.
- Before you run `ilot restart` with no app name, ask the user. It stops every app of every stack and the ilot daemon, the user's other sessions included, then starts again the apps that ran.
- Never run `ilot daemon run`. `ilot` starts its daemon by itself.

## Stacks

A stack is the set of apps that run for one ticket.

- `ilot` names the stack after the ticket key in the branch name: `PROJ-`, `OPS-` or `TASK-` and digits, in lowercase. The branch `dev/jdoe/task-2006-example` belongs to the stack `task-2006`. Without a ticket key in the branch, `ilot` looks in the folder name, then uses the folder name.
- The main checkout of a repo is the `master` stack.
- Shared apps (for example Temporal, auth-mock, restapi) run once, in the `devbox` stack.
- The legacy apps do not start at boot. Start restapi with `ilot start restapi`, and the legacy web stack (apache and legacyweb) with `ilot start legacy-stack`.
- A workspace is a root repo and its member repos. The `ilot.yaml` of the root lists the members under `repos`, and the `ilot.yaml` of a member names the root with `member_of`. `ilot` finds the workspace from the repo of the current folder.
- A stack takes each repo's worktree for its ticket. If a repo has no worktree for the ticket, the stack takes that repo's main checkout.
- If a worktree does not start an app, the app comes from the `master` stack, and `ilot` starts it there if necessary. Apps marked `per_stack` (for example portal's api, worker and catalog) always run in the stack itself.

Run every `ilot` command inside a repo of the workspace: outside one, `ilot` does not find the workspace and says so. `ilot start`, `ilot stop`, `ilot logs`, `ilot apps` and `ilot env` act on the stack of the folder that you are in. `ilot ls`, `ilot rm` and `ilot clean` act on every stack.

## Commands

```bash
ilot ls                         # every app of every stack: status, ports, proxy URL; then the worktrees of each ticket stack
ilot ls --json                  # the same as one JSON object, with the checkout of each repo per stack: use it to read the list from a script
ilot apps                       # every app and external service that the ilot.yaml files declare for this folder's stack, with the dependencies of each app
ilot apps --json                # the same as one JSON object
ilot start portal-front         # build, then start the app after its dependencies, in this folder's stack
ilot start publicapi --no-deps  # start only this app
ilot stop publicapi             # stop the app in this folder's stack
ilot logs publicapi             # the last 100 lines of the app's log in this stack; a JSON line becomes one readable line
ilot logs --json publicapi      # the app's own lines, as they are: use this to read logs
ilot logs -f -n 20 publicapi    # the last 20 lines, then the new ones: it never ends, so run it in the background, never in a foreground command
ilot env portal-api             # the environment of the app, with where each value comes from
ilot check                      # check the ilot.yaml of this folder's repo, with the other repos' ilot.yaml
ilot check path/to/ilot.yaml     # check that file
ilot rm task-2006               # stop a stack and delete its state, its logs and its routes (ask first)
ilot clean                      # ilot rm on every stack whose worktrees are deleted (ask first)
ilot boot --checkout <main checkout of a repo of the workspace>  # starts again the apps that ran before a restart of the machine; you do not need it
ilot restart publicapi          # build the app again, then restart it, in this folder's stack; it must run already
ilot restart                    # with no app: stop every app of every stack and the ilot daemon, start a new daemon, then start the apps that ran (ask first)
ilot daemon run                 # the daemon itself: ilot starts it, never run it
ilot --version                  # the version of ilot
ilot skill print                # print this skill
ilot skill install              # copy this skill to ~/.claude/skills/ilot/SKILL.md
```

`ilot start` rebuilds an app that already runs, then restarts it. An app with `watch` in its `ilot.yaml` (the Go APIs: internalapi, publicapi, billingapi) is rebuilt and restarted when its files change: you do not need `ilot start` after an edit. If its build fails, the old version keeps running, and the error is in `ilot logs <app>-watch`. For other apps, after a code change, run `ilot start <app>` again.

## Where things are

- Proxy URLs: `http://<app>.<stack>.<workspace>.localhost:4580`. `ilot ls` prints them. The page `http://localhost:4580` lists them all. The URL of an app that does not run answers 503 with the command that starts it.
- State and logs: `~/.local/state/ilot/stacks/<stack>/`, logs in `logs/<app>.log`.
- Apps: the `ilot.yaml` at the root of each repo. `ilot apps` lists their names, their repo and their dependencies. A checkout with no `ilot.yaml` reads the file of the main checkout of its repo.
- The personal file: `~/.config/ilot/env.yaml`. It holds the path of a member repo that is cloned at another place (`repos.<name>.path`), and personal values (`env`, and `apps.<app>` for one app). Do not put secrets in a repo file: put them there.
- An app gets a fixed environment. The values of the shell files come first: `ilot` reads the whole environment of a login shell (`$SHELL -l`), and `ilot env` marks these values `(shell)`. A login shell reads the profile files, for example `~/.zprofile`, and not `~/.zshrc`: if an app does not find a tool, compare the `PATH` of `ilot env <app>` with the `PATH` of the terminal. The `ilot.yaml` and personal values come on top. An `export`, an `unset` or a `.envrc` in a shell never reaches an app: put the value in the personal file. An app gets its environment when it starts: after a change, run `ilot start <app>` again. If a command prints `warning: the ilot daemon has the version`, the daemon is older than `ilot`. Ask the user before you run `ilot restart`.
- The README of ilot describes every key and template of `ilot.yaml`.

## When you edit an ilot.yaml

Run `ilot check` after every change to a `ilot.yaml`. It merges the file with the other repos' `ilot.yaml`, runs the config checks, and renders every app's command, env and mounts. It finds typos in fields and templates, and duplicate app names.

When you review a PR that changes a `ilot.yaml`, run `ilot check` on it too.

## When a start fails

1. Read the error that `ilot start` prints. It names the app and its log file.
2. Run `ilot logs --json <app>` and read the last lines.
3. Run `ilot env <app>` to see the ports and the values it got.
4. Run `ilot ls` to see which dependencies run and where.

`ilot start` checks the container images before any build. It pulls a missing image (`pulling <image>`), once for all apps that share it. A failed pull stops `ilot start` before the builds. The error is `pull <image>: <error>`, and it can end with a hint:

- `log in to the registry, then run the command again: docker login <registry>`
- `docker could not read its credential store: unlock your GPG key, for example with echo test | gpg --clearsign >/dev/null, then run the command again`

Both hints need the user (a login, or the GPG passphrase). Tell the user, wait, then run the same `ilot start` again.

When an app that this run started ends, `ilot start` stops it and marks it stopped. Its log stays. Run the same `ilot start` again. It starts that app again. It also starts again a dependency of the named apps in `master` or in the `devbox` stack, without its name. A dependency that ran in the ticket stack because an earlier `ilot start` named it is different: after `ilot` stops it, a plain retry starts it in `master`. The error then ends with `<app> ran in <stack>: name it to start it there again: ilot start <app> <apps>`. Run that command. A dependency that already ran, and that `ilot start` only waited for, is never stopped.

When the timeout passes while the app still runs, the error ends with `it still starts: ilot keeps it`. The app stays. Do not run `ilot start` again for it: that restarts a named app from zero. Follow its log with `ilot logs -f <app>` until it is ready, and check with `ilot ls` that it still runs. Next time, use a longer `--timeout`. Ctrl-C keeps the app too.

`ilot boot` does the same image check. A failed image fails only that app and the apps that depend on it.

A port that "is taken by another process" belongs to something outside `ilot`. Find it, and ask the user before you stop it.

## Traps

- Two worktrees of one repo for the same ticket make `ilot` refuse that stack. The error names both folders. Keep one.
- A container app declares its ports in `container.ports`, with the port inside the container. `ilot` publishes each one on `127.0.0.1` on a port of the stack, which `ilot ls` and `ilot env` show. A container reaches another app with `{{host "<app>"}}`, which gives it `host.docker.internal`.
- If you test a build of `ilot` with a separate `XDG_STATE_HOME`, also set `ILOT_PROXY_LISTEN` to a free address, for example `127.0.0.1:4590`. Without it, the scratch daemon cannot open the proxy port that the real daemon holds.
- If a command prints `warning: the proxy cannot listen`, another program has the proxy port. The apps run, but their URLs do not answer. Tell the user.
- In zsh, a shell variable named `path` overwrites `PATH`. Name loop variables something else.
