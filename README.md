<p align="center">
  <img src="docs/mark.svg" width="112" alt="The ilot mark: the o of ilot as a lagoon, half filled with water">
</p>

# îlot

Every git worktree gets its own island of apps.

`ilot` builds and starts the apps of each git worktree, gives each app its own ports, and puts it behind one local proxy.
You can work on two tickets at the same time, and each ticket runs its own copy of the apps that it changes.
The apps that a ticket does not change come from the `master` stack, which runs from the main checkout.

The name comes from the French word "îlot", a small island. The command and the repo spell it `ilot`.

## How it works

A stack is the set of apps that run for one ticket.
The main checkout of a repo is the `master` stack.
`ilot` names the stack of a linked worktree after the ticket key in its branch name: `PROJ-`, `OPS-` or `TASK-` and digits, in lowercase.
For example, the branch `feature/PROJ-1234-retry-jobs` belongs to the stack `proj-1234`.
If the branch has no ticket key, `ilot` looks for one in the folder name, and else it uses the folder name.
The ticket keys are fixed in `internal/stack/stack.go`.

A worktree runs only the apps that you start in it.
The other dependencies come from the `master` stack. If one of them does not run, `ilot` starts it there.
An app marked `per_stack` is the exception: an app that depends on it always gets the copy of its own stack.
A shared app runs once, in the `devbox` stack, for every worktree.

`ilot` knows several repos.
For each repo, a stack takes the worktree for its ticket. If the repo has no such worktree, the stack takes the main checkout of the repo.
Each stack reads the `ilot.yaml` files of its own checkouts, so a change in one stack does not change the apps of another.
Two worktrees of one repo for the same ticket make `ilot` refuse that stack, and the error names both folders.

Each app gets its ports in the range 20000-29999 and keeps them.
Its URL, `http://<app>.<stack>.<domain>:<proxy port>`, therefore stays the same.
One process-compose daemon supervises the apps of every stack.
A Traefik proxy sends each URL to its app.

## Requirements

`ilot` expects a specific development environment. The code uses each item below.

- Linux, and Go 1.26 to build `ilot`.
- A development container, the devbox, that holds the file `/etc/devhost.env`. Without that file, `ilot` refuses to run, except for `ilot check` and `ilot apps`.
- The Docker daemon of the host of the devbox, the devhost. `ilot` turns a path under your devbox home into the same path under `/home/<login>/devbox` on the devhost, so that bind mounts work. `LC_DEVBOX_LOGIN` gives `<login>`.
- process-compose on `PATH`.
- zsh. `ilot` reads the environment of the apps from a clean zsh.
- A Traefik container named `ilotproxy_<login>` that mounts its routes folder at `/etc/traefik/routes`. To skip that lookup, set `ILOT_ROUTES_DIR` to the routes folder.
- The port of that proxy in `ILOT_PROXY_PORT`, or else in `DEVHOST_PORT_7`.
- A wildcard DNS name, `*.<domain>`, that points to the proxy.
- The `temporal` CLI, only for apps with `temporal_namespace`.
- mise, only for repos with a `mise.toml` at the root.

## Install

From a clone of this repo:

```bash
go install .
```

The binary goes to `$(go env GOPATH)/bin`. Make sure that this folder is on your `PATH`.

## Quick start

1. Clone your main repo, the platform repo, at `~/src/platform`. If you clone it at another path, set that path in your personal file (see "Personal settings").
2. Write `devbox/ilot.yaml` in the platform repo. This file also lists the other repos:

   ```yaml
   domain: ilot.test
   repos:
     web: {path: ~/src/web, config: ilot.yaml}
   external:
     postgres:
       port: 5432
       start_hint: run docker compose up -d postgres
   apps:
     api:
       build: go build -o {{.BinDir}}/api ./cmd/api
       run: "{{.BinDir}}/api --port={{.Ports.http}}"
       env:
         DATABASE_URL: postgres://localhost:{{port "postgres"}}/app
       ports: [http]
       route: http
       ready: {path: /health}
       depends_on: [postgres]
   ```

3. Write `ilot.yaml` at the root of the web repo:

   ```yaml
   apps:
     front:
       build: npm ci
       run: npm run dev -- --port {{.Ports.http}}
       env:
         API_URL: '{{url "api"}}'
       ports: [http]
       route: http
       depends_on: [api]
   ```

4. Run `ilot check` in each repo. It merges the file with the files of the other repos and renders every template.
5. Run `ilot start api` in `~/src/platform`. `ilot` builds `api`, starts it, and waits until `/health` answers.
6. Run `ilot ls` to see the status and the URL of each app.

To work on a ticket, add a worktree and start the apps that you change in it:

```bash
git -C ~/src/platform worktree add ~/worktrees/platform-1234 -b PROJ-1234-retry-jobs
cd ~/worktrees/platform-1234
ilot start api front
```

`api` now runs twice: in `master` from the main checkout, and in `proj-1234` from the worktree.
`front` runs in `proj-1234` from the main checkout of the web repo, and its `API_URL` points to the `api` of `proj-1234`.

## Commands

`ilot start`, `stop`, `logs`, `apps` and `env` act on the stack of the folder that you are in.
Outside any checkout, they act on the `master` stack and say so.
`ilot ls`, `rm` and `clean` work from any folder.

```bash
ilot ls                       # every app of every stack: status, ports, URL; then the worktrees of each ticket stack
ilot ls --json                # the same as one JSON object
ilot apps                     # every app and external service that the ilot.yaml files of this stack declare, with the dependencies of each app
ilot apps --json              # the same as one JSON object
ilot start api                # build, then start the app after its dependencies
ilot start api --no-deps      # start only this app
ilot stop api                 # stop the app in this stack
ilot logs api                 # the last 100 lines of the log of the app
ilot logs --json api          # the lines of the app as it wrote them
ilot logs -f -n 20 api        # the last 20 lines, then the new ones
ilot env api                  # the environment of the app, with the source of each value
ilot check                    # check the ilot.yaml of this repo with the files of the other repos
ilot check path/to/ilot.yaml  # check that file
ilot rm proj-1234             # stop a stack and delete its state, logs, routes and Temporal namespace
ilot clean                    # ilot rm on each stack whose worktrees are deleted
ilot restart                  # stop every app, start the daemon again with a fresh environment, then start the apps that ran
ilot boot --checkout <dir>    # start the autostart apps and the apps that ran: run it at login
ilot --version
ilot --skill                  # print the skill for AI agents
```

`ilot start` starts the missing dependencies first, and waits until each one is ready.
`ilot start` also rebuilds an app that already runs, then restarts it.
When an app that `ilot start` started ends, `ilot start` stops it, marks it stopped and keeps its log.
When the timeout passes while the app still runs, `ilot` keeps the app and says so.

## The ilot.yaml files

The root file is `devbox/ilot.yaml` in the main checkout of the platform repo.
Only this file sets `domain`, `gateway_base_file` and `repos`.
Each entry of `repos` gives the default path of a repo and, for a repo with apps, the name of its file.
Every repo can declare `env`, `external` and `apps`.

The fields of an app:

| Field | Meaning |
|---|---|
| `run` | Command that starts the app. Required, except for a container. |
| `build` | Command that builds the app. `ilot start` runs it in the checkout before it starts the app. |
| `env` | Environment variables of the process. |
| `dir` | Working folder of the process. A relative path is relative to the checkout. The default is the checkout. |
| `ports` | Names of the ports of the app. `ilot` gives each name a free port in 20000-29999 and keeps it. |
| `fixed_ports` | Fixed numbers for some of the ports, for example `{http: 9090}`. On a normal app, they apply to the `master` stack only. |
| `route` | Port that the proxy URL sends requests to. |
| `home_path` | Path that the root of the proxy URL redirects to, for example `/swagger/`. It needs a `route`. |
| `ready` | How `ilot` knows that the app is ready: `port`, and `path` for an HTTP check. Without `path`, `ilot` waits until the port accepts a connection. The default port is the `route` port. A `command` replaces the port check: `ilot` runs it until it exits with 0. |
| `depends_on` | Apps and external services that the app needs at startup. |
| `gateway_aliases` | Names that other apps use to call this app through the gateway. It needs a `route`. |
| `gateway` | Marks the gateway app. Only one app can have it. |
| `autostart` | `ilot boot` starts the app in the `master` stack. |
| `shared` | The app runs once, in the `devbox` stack, for every worktree. Each of its ports needs a number in `fixed_ports`. |
| `per_stack` | An app that depends on this app gets the copy of its own stack, not the `master` copy. |
| `watch` | If the files of the app change, `ilot` restarts the app. List the files with `paths`, relative to the working folder, and file extensions in `exts`. An app with a `build` is built again first. If the build fails, the app keeps running and the error is in `ilot logs <app>-watch`. |
| `temporal_namespace` | Before each start, `ilot` creates a Temporal namespace named after the stack. |
| `container` | Runs the app as a Docker container. See "Container apps". |

The `build`, `run` and `env` values can use these templates:

| Template | Value |
|---|---|
| `{{.Ports.<name>}}` | The port of this app with that name. |
| `{{port "<app>" "<name>"}}` | The port of another app or of an external service. If the stack runs its own copy of the app, it gets the port of that copy. Else it gets the port of the `master` copy. |
| `{{url "<app>"}}` | The proxy URL of an app, with the same rule. |
| `{{host "<app>"}}` | The host name that this app uses to reach another app: the container name of a container, `devbox` for a process called from a container, and `localhost` for a process called from a process. |
| `{{repo "<name>"}}` | The checkout of another repo for this stack. |
| `{{.WorktreeDir}}` | The checkout of the repo of the app for this stack. |
| `{{.BinDir}}` | The folder for the binaries of the stack. |
| `{{.StackDir}}` | The folder for the state and the logs of the stack. |
| `{{.Stack}}` | The name of the stack. |
| `{{.User}}` | Your login. |
| `{{.Domain}}` | The domain of the proxy URLs. |
| `{{.ProxyPort}}` | The port of the proxy. |

An app never chooses its own port. It reads the port from a flag or a variable that its `run` command gives it.

An external service is a service that `ilot` does not start. It has a `port`, a `start_hint` and optional `gateway_aliases`.
`ilot start` makes sure that its port answers. If the port does not answer, `ilot start` prints the `start_hint`.

### The gateway

One app can be the gateway of a stack.
For each stack, `ilot` writes `services.json` in the folder of the stack.
This file maps the `gateway_aliases` of the running apps to their addresses, on top of the file that `gateway_base_file` names.
Give that path to your gateway with `{{.StackDir}}/services.json`.

### Container apps

An app with a `container` block runs as a Docker container on the `ilotproxy` network, named `ilot-<stack>-<app>`.
It needs no `run`. If the app has a `build`, it runs before the container starts.

| Field | Meaning |
|---|---|
| `image` | Image to run. Required. |
| `mounts` | Folders to mount, from the path in the container to the path in the devbox. The value can use templates. |
| `env_files` | Env files that `docker run` reads. |
| `links` | Other container apps of the same stack, from the host name that the container uses to the app name. `ilot start` starts them first. |
| `external_links` | Containers that `ilot` does not run, from the host name that the container uses to the container name. |

Each port of a container needs a number in `fixed_ports`. It is the port inside the container, so two stacks can use the same number.
The `env` of the app goes to the container with `-e KEY`, so the values stay out of the command line.
`ilot` pulls a missing image before any build, one time for all the apps that use it.

## Personal settings

Settings that differ from person to person go in `~/.config/ilot/env.yaml`, outside every repo.
Make it readable only by you (`chmod 600`), because it can hold tokens.

```yaml
env:                      # every app of every repo
  LOG_LEVEL: debug
apps:                     # one app only
  front:
    API_TOKEN: <token>
repos:
  platform: {path: ~/code/platform}            # a repo cloned at another path
  mytool: {path: ~/mytool, config: ilot.yaml}  # a personal repo, on this machine only
```

`ilot` builds the environment of an app in this order, and the last value wins:

1. The shell files. `ilot` runs a clean zsh and keeps `HOME`, `PATH`, `USER`, `LOGNAME` and `LANG`. It also keeps the variables of a fixed list in `internal/shellenv/shellenv.go`. Nothing of your own shell gets in.
2. Defaults: `APP_NAME` is your login, and `VSCODE_REPO_PATH` is the checkout of the app.
3. `KEYRING_APP_KEY`, read from `~/.keyring` at each start.
4. The `ilot.yaml` of the repo: its top-level `env`, then the `env` of the app.
5. Your personal file: its `env`, then its entry under `apps`.
6. The values that `ilot.yaml` writes with a template. Your personal file cannot replace them.

A container app does not get the top-level `env` of your personal file, so a third-party image does not get your tokens. It gets its entry under `apps`.
The daemon keeps the shell values that it started with. To give the apps fresh values, run `ilot restart`.

## For AI agents

`ilot` ships a skill for Claude Code, `skill/SKILL.md`.
It tells an agent that `ilot` is its own tool, and it describes every command, the stacks and the traps.
`ilot boot` copies it to `~/.claude/skills/ilot/SKILL.md`, and `ilot --skill` prints it.
The skill pre-approves every `ilot` command.
When you add a command, add it to the skill too: a test fails otherwise.

## Files and folders

| Path | Content |
|---|---|
| `~/.config/ilot/env.yaml` | Your personal settings. |
| `~/.local/state/ilot/stacks/<stack>/` | The state of a stack, its binaries in `bin/`, its logs in `logs/<app>.log` and its `services.json`. |
| `~/.local/state/ilot/process-compose/` | The state of the daemon, and the environment that it started with. |
| `<routes folder>/ilot-<stack>.yml` | The proxy routes of a stack. |
| `~/.cache/ilot/` | The builds of `ilot` itself (see below). |

A log rotates at 10 MB, and `ilot` keeps the 2 previous files.
After you delete the worktrees of a ticket, run `ilot clean` to give back its ports.

## Building ilot from the platform repo

`ilot` can live inside the platform repo, at `devbox/ilot`.
A copy of `ilot` built with a stamped source tree then builds `ilot` again from the main checkout, after each pull that changes that folder.
It keeps that build in `~/.cache/ilot/`.
`ilot rebuild` shows the full build output, and `ilot rebuild --local` builds the uncommitted files too.
A plain `go install` turns this off.

## Development

```bash
go vet ./...
go test ./...
```
