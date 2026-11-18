---
name: dev
description: "Run, stop, inspect and debug the apps of devbox with the dev CLI. Use it inside devbox for any task about an app, a port, a log, a proxy URL, a stack, a dev.yaml or dev itself. Examples: start an API to test a change, read its logs, find its URL, check a dev.yaml."
allowed-tools: Bash(dev:*)
---

# dev

`dev` runs the apps of devbox: the platform APIs, portal, Temporal, catalog and the other services. It starts each app with its dependencies, gives it ports, puts it behind the dev proxy and keeps its logs.

## The rule

Inside devbox, `dev` is yours to run. Run `dev start`, `dev stop`, `dev logs`, `dev ls`, `dev apps`, `dev env` and `dev check` yourself. Do not ask the user to run them, and do not tell the user to "start the API": start it.

Every `dev` command is pre-approved for you. Two limits still apply:

- Never run `make up` or `make down`, never recreate or restart the devbox container, and never edit `devbox/.env`. Recreating devbox ends every session inside it, yours included. If one of these is necessary, ask the user.
- Before you run `dev rm` or `dev clean` on a stack that you did not start, ask the user. Both delete the stack's state, logs, containers and Temporal namespace.
- Before you stop a shared app (for example Temporal, auth-mock, restapi) or a `master` app that you did not start, ask the user. Other stacks and the user's other sessions use them.
- Before you run `dev restart`, ask the user. It stops every app of every stack, the user's other sessions included, then starts again the apps that ran.

## Stacks

A stack is the set of apps that run for one ticket.

- `dev` names the stack after the ticket key in the branch name: `PROJ-`, `OPS-` or `TASK-` and digits, in lowercase. The branch `dev/jdoe/task-2006-example` belongs to the stack `task-2006`. Without a ticket key in the branch, `dev` looks in the folder name, then uses the folder name.
- The main checkout of a repo is the `master` stack.
- Shared apps (for example Temporal, auth-mock, restapi) run once, in the `devbox` stack.
- The legacy apps do not start at boot. Start restapi with `dev start restapi`, and the legacy web stack (apache and legacyweb) with `dev start legacy-stack`.
- A stack takes each repo's worktree for its ticket. If a repo has no worktree for the ticket, the stack takes that repo's main checkout.
- If a worktree does not start an app, the app comes from the `master` stack, and `dev` starts it there if necessary. Apps marked `per_stack` (for example portal's api, worker and catalog) always run in the stack itself.

`dev start`, `dev stop`, `dev logs`, `dev apps` and `dev env` act on the stack of the folder that you are in. Outside any checkout, they act on `master` and say so. A home folder that is itself a git checkout, for example a dotfiles repo, counts as outside any checkout. `dev ls`, `dev rm` and `dev clean` work from any folder.

## Commands

```bash
dev ls                         # every app of every stack: status, ports, proxy URL; then the worktrees of each ticket stack
dev ls --json                  # the same as one JSON object, with the checkout of each repo per stack: use it to read the list from a script
dev apps                       # every app and external service that the dev.yaml files declare for this folder's stack, with the dependencies of each app
dev apps --json                # the same as one JSON object
dev start portal-front         # build, then start the app after its dependencies, in this folder's stack
dev start publicapi --no-deps  # start only this app
dev stop publicapi             # stop the app in this folder's stack
dev logs publicapi             # the last 100 lines of the app's log in this stack
dev logs --json publicapi      # the app's own lines, as they are: use this to read logs
dev logs -f -n 20 publicapi    # the last 20 lines, then the new ones: it never ends, so run it in the background, never in a foreground command
dev env portal-api             # the environment of the app, with where each value comes from
dev check                      # check the dev.yaml of this folder's repo, with the other repos' dev.yaml
dev check path/to/dev.yaml     # check that file
dev rm task-2006               # stop a stack and delete its state, logs, routes and Temporal namespace (ask first)
dev clean                      # dev rm on every stack whose worktrees are deleted (ask first)
dev boot --checkout <main checkout>  # runs at login; you do not need it
dev restart                    # stop every app of every stack, start dev again with a fresh environment from the shell files, then start the apps that ran (ask first)
dev bootstrap               # the user runs it, once: it asks for secrets. If npm, jf or docker login fail with 401, tell the user to run it. Never run it yourself.
dev rebuild                    # build dev from the main checkout again, with the full output
dev --version                  # the version of dev: (checkout) or (image)
dev --skill                    # print this skill
```

`dev start` rebuilds an app that already runs, then restarts it. An app with `watch` in its `dev.yaml` (the Go APIs: internalapi, publicapi, billingapi) is rebuilt and restarted when its files change: you do not need `dev start` after an edit. If its build fails, the old version keeps running, and the error is in `dev logs <app>-watch`. For other apps, after a code change, run `dev start <app>` again.

## Where things are

- Proxy URLs: `http://<app>.<stack>.<domain>:<proxy port>`. `dev ls` prints them. The home page `http://home.<domain>:<proxy port>` lists them all.
- State and logs: `~/.local/state/dev/stacks/<stack>/`, logs in `logs/<app>.log`.
- Apps: the `dev.yaml` of each repo. `dev apps` lists their names, their repo and their dependencies. In platform, `devbox/dev.yaml`, which also holds the `repos:` list. In portal, `dev.yaml` at the root.
- The personal file: `~/.config/dev/env.yaml`. It holds repo paths for clones in other places, and personal values (`env`, and `apps.<app>` for one app). A `repos` entry with a `path` and a `config` adds a personal repo: its `dev.yaml` adds apps on this machine only. Do not put secrets in a repo file: put them there.
- An app gets a fixed environment. The values of the shell files come first: `PATH`, `HOME`, the DEVHOST ports and the Go variables, read by a clean shell and marked `(shell)` by `dev env`. `KEYRING_APP_KEY` comes from `~/.keyring`, read at each start. The `dev.yaml` and personal values come on top. An `export`, an `unset` or a `.envrc` in a shell never reaches an app: put the value in the personal file. If `dev env` prints `note: dev started before it fixed the environment of the apps`, the apps run with the values of an old shell. Ask the user before you run `dev restart`.
- The README of the platform repo describes every field and template of `dev.yaml`.

## When you edit a dev.yaml

Run `dev check` after every change to a `dev.yaml`. It merges the file with the other repos' `dev.yaml`, runs the config checks, and renders every app's command, env and mounts. It finds typos in fields and templates, duplicate app names and gateway aliases claimed twice.

When you review a PR that changes a `dev.yaml`, run `dev check` on it too: CI checks only platform's file.

## When a start fails

1. Read the error that `dev start` prints. It names the app and its log file.
2. Run `dev logs --json <app>` and read the last lines.
3. Run `dev env <app>` to see the ports and the values it got.
4. Run `dev ls` to see which dependencies run and where.

`dev start` checks the container images before any build. It pulls a missing image (`pulling <image>`), once for all apps that share it. A failed pull stops `dev start` before the builds. The error is `pull <image>: <error>`, and it can end with a hint:

- `log in to the registry, then run the command again: docker login <registry>`
- `docker could not read its credential store: unlock your GPG key, for example with echo test | gpg --clearsign >/dev/null, then run the command again`

Both hints need the user (a login, or the GPG passphrase). Tell the user, wait, then run the same `dev start` again.

When an app that this run started ends, `dev start` stops it and marks it stopped. Its log stays. Run the same `dev start` again. It starts that app again. It also starts again a dependency of the named apps in `master` or in the `devbox` stack, without its name. A dependency that ran in the ticket stack because an earlier `dev start` named it is different: after `dev` stops it, a plain retry starts it in `master`. The error then ends with `<app> ran in <stack>: name it to start it there again: dev start <app> <apps>`. Run that command. A dependency that already ran, and that `dev start` only waited for, is never stopped.

When the timeout passes while the app still runs, the error ends with `it still starts: dev keeps it`. The app stays. Do not run `dev start` again for it: that restarts a named app from zero. Follow its log with `dev logs -f <app>` until it is ready, and check with `dev ls` that it still runs. Next time, use a longer `--timeout`. Ctrl-C keeps the app too.

`dev boot` does the same image check. A failed image fails only that app and the apps that depend on it.

A port that "is taken by another process" belongs to something outside `dev`. Find it, and ask the user before you stop it.

## Traps

- Two worktrees of one repo for the same ticket make `dev` refuse that stack. The error names both folders. Keep one.
- A container app's port is the port inside the container, so the same number can appear in several stacks.
- If you test a build of `dev` with a separate `XDG_STATE_HOME`, also set `DEV_ROUTES_DIR` to a scratch folder. Without it, the scratch state rewrites the real proxy routes.
- In zsh, a shell variable named `path` overwrites `PATH`. Name loop variables something else.
