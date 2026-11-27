<p align="center">
  <img src="docs/mark.svg" width="112" alt="The ilot mark: the o of ilot as a lagoon, half filled with water">
</p>

# îlot

Every git worktree gets its own island of apps.

`ilot` builds and starts the apps of each git worktree, gives each app its own ports, and puts it behind one local proxy.
You can work on two tickets at the same time, and each ticket runs its own copy of the apps that it changes.
The apps that a ticket does not change come from the `base` stack, which runs from the main checkout.

The name comes from the French word "îlot", a small island. The command and the repo spell it `ilot`.

## How it works

A stack is the set of apps that run for one ticket.
The main checkout of every repo is the `base` stack, on any branch.
`ilot` names the stack of a linked worktree after its branch: the branch name in lowercase letters, digits and dashes, 63 characters at most.
For example, the branch `feature/retry-jobs` belongs to the stack `feature-retry-jobs`.
The branch is the name because one ticket usually gets the same branch name in each repo.
With `stack.pattern` in the `ilot.yaml` of the root repo, the name is the first match of that regular expression in the branch name. With `pattern: '[A-Za-z]+-[0-9]+'`, the branch `feature/PROJ-1234-retry-jobs` belongs to the stack `proj-1234`. A branch that the pattern does not match keeps its whole name.
A worktree with a detached HEAD takes the name of its folder.

A worktree gets its stack one time, at the first `ilot start` in it. `ilot` records the pair of the worktree and the stack. This is the binding.
After the binding, `git switch`, a rebase or a bisect in the worktree does not move it to another stack.
`ilot stack show` prints the stack of the current worktree. `ilot stack bind <name>` sets or changes the binding by hand.

A worktree runs only the apps that you start in it.
The other dependencies come from the `base` stack. If one of them does not run, `ilot` starts it there.
The `scope` of an app can fix that choice:

| `scope` | Meaning |
|---|---|
| absent | If the stack runs its own copy of the app, an app that depends on it gets that copy. If not, it gets the copy of the `base` stack. |
| `stack` | An app that depends on it always gets the copy of its own stack. |
| `base` | The app runs one time, in the `base` stack, for every stack. |

A workspace is the set of repos that share stacks, ports and one name.
One repo is the root repo: the `ilot.yaml` at its root names the workspace and lists the other repos.
Each other repo is a member: its `ilot.yaml` names the root with `member_of`.
`ilot` finds the workspace from the repo of the folder that you are in, so it needs no registry.
Several workspaces can run on one machine. Two workspaces with the same name get an error that tells you to set `name`.

For each repo, a stack takes the worktree that is bound to it. If the repo has no such worktree, the stack takes the main checkout of the repo.
A worktree of a second repo on the same branch joins the stack at the first `ilot start` in it, because the branch gives the same name. If its branch has another name, run `ilot stack bind <name>` in it.
Each stack reads the `ilot.yaml` files of its own checkouts, so a change in one stack does not change the apps of another.
A checkout with no `ilot.yaml` reads the file of the main checkout. An untracked `ilot.yaml` in the main checkout therefore works in a repo where you cannot commit one.
A stack takes one worktree of a repo. A second one gets an error that names the first, and `ilot stack bind <name>` gives it another stack.

Each app gets its ports in the range 20000-29999 and keeps them. The global file can set another range.
Its URL, `http://<app>.<stack>.<workspace>.localhost:4580`, therefore stays the same.
One daemon supervises the apps of every stack. It is the `ilot` binary itself, and `ilot` starts it when a command needs it.
The same daemon holds the proxy: it listens on `127.0.0.1:4580` and sends each URL to its app.
Every name under `localhost` points to this machine, so the URLs need no DNS setup.
A URL of an app that does not run answers with the command that starts the app. Any other URL on that port, for example `http://localhost:4580`, lists every route.

## Requirements

- macOS or Linux, and Go 1.26 to build `ilot`.
- git.
- Docker, for container apps only. A mount of a container app names a path on this machine.

## Install

From a clone of this repo:

```bash
go install .
```

The binary goes to `$(go env GOPATH)/bin`. Make sure that this folder is on your `PATH`.

## Quick start

1. Clone the two repos of this example side by side: `~/code/shop` and `~/code/web`.
2. Write `ilot.yaml` at the root of the shop repo. It is the root repo, so this file also lists the other repos:

   ```yaml
   name: shop                # optional: the default is the folder name of the main checkout
   repos:
     web: ../web             # relative to the main checkout of this repo
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

3. Write `ilot.yaml` at the root of the web repo. It is a member repo, so it names the root:

   ```yaml
   member_of: ../shop
   wrap: mise exec --
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
5. Run `ilot start api` in `~/code/shop`. `ilot` builds `api`, starts it, and waits until `/health` answers.
6. Run `ilot ls` to see the status and the URL of each app.

To work on a ticket, add a worktree and start the apps that you change in it:

```bash
git -C ~/code/shop worktree add ~/code/shop-retry -b retry-jobs
cd ~/code/shop-retry
ilot start api front
```

`api` now runs twice: in `base` from the main checkout, and in `retry-jobs` from the worktree.
`front` runs in `retry-jobs` from the main checkout of the web repo, and its `API_URL` points to the `api` of `retry-jobs`.

## Commands

Every command finds the workspace from the repo of the folder that you are in, so run `ilot` inside a repo of the workspace.
`ilot start`, `stop`, `logs`, `apps`, `env` and `stack` act on the stack of that folder.
`ilot ls`, `rm` and `clean` act on every stack.

```bash
ilot ls                       # every app of every stack: status, ports, URL; then the worktrees of each ticket stack
ilot ls --json                # the same as one JSON object
ilot apps                     # every app and external service that the ilot.yaml files of this stack declare, with the dependencies of each app
ilot apps --json              # the same as one JSON object
ilot start api                # build, then start the app after its dependencies
ilot start api --no-deps      # start only this app
ilot start --all              # start every app that a stack owns and that does not run, for example after the daemon crashed
ilot stop api                 # stop the app in this stack
ilot logs api                 # the last 100 lines of the log of the app; a JSON line becomes one readable line
ilot logs --json api          # the lines of the app as it wrote them
ilot logs -f -n 20 api        # the last 20 lines, then the new ones
ilot env api                  # the environment of the app, with the source of each value
ilot check                    # check the ilot.yaml of this repo with the files of the other repos
ilot check path/to/ilot.yaml  # check that file
ilot rm retry-jobs            # stop a stack and delete its state, its logs and its routes
ilot clean                    # ilot rm on each stack whose bound worktrees are all deleted
ilot stack show               # the stack of this worktree, and the checkout of each repo for it
ilot stack bind retry-jobs    # put this worktree in the stack retry-jobs
ilot restart api              # build the app again, then restart it; its dependencies stay as they are
ilot restart                  # with no app: stop every app and the daemon, start a new daemon, then start the apps that ran
ilot boot --checkout <dir>    # start the autostart apps and the apps that ran before a restart of the machine: run it at login, with the main checkout of a repo of the workspace
ilot --version
ilot skill print              # print the skill for AI agents
ilot skill install            # copy the skill to ~/.claude/skills/ilot/SKILL.md
ilot daemon run --state-dir <dir>  # the daemon itself: ilot starts it, you do not need it
```

`ilot ls` gives each app one of these statuses: `starting`, `ready`, `stopping`, `exited (<exit code>)`, `down` for an app that its stack owns and that the daemon does not hold, for example after a crash of the daemon, or `stopped` for an app that no stack owns.
An app that no `ilot.yaml` of its stack declares any more is marked `undeclared`. It keeps its ports until `ilot stop <app>`, in its stack, removes it from the stack.
Before it starts an app, `ilot start` tests the ports that the app already has. If another program took one, the error names the port.
When an app is `down`, `ilot ls` ends with the command that starts it again: `ilot start --all`. It starts each app that a stack owns and that does not run, after its dependencies, and leaves the others as they are.

`ilot start` starts the missing dependencies first, and waits until each one is ready.
`ilot start` also rebuilds an app that already runs, then restarts it.
A command calls the daemon only for the apps that it starts, restarts or stops. A dependency that runs gets no call, and an app of another stack never restarts.
When the start changes a template of a running app of the same stack, for example because the stack now runs its own copy of a dependency, `ilot start` restarts that app and prints one line for it.
Only one build runs in a stack at a time. A second build, from another `ilot` command or from a watcher, waits and says so.
The stack owns every app of a start from the first line: an app that ends, or that fails to start, stays in the stack. `ilot ls` shows it `exited`, its log stays, and the next `ilot start` starts it again in the same stack.
When the timeout passes while the app still runs, `ilot` keeps the app and says so.
`ilot stop`, `ilot rm`, `ilot ls` and `ilot logs` need the state only, so they work when an `ilot.yaml` is broken. A start reads the files of its own stack and of `base` only.

## The ilot.yaml files

The file of a repo is `ilot.yaml` at the root of the repo. Its name and its place are fixed.

These keys describe the workspace. `ilot` reads them from the main checkout of a repo only:

| Key | In | Meaning |
|---|---|---|
| `name` | the root | The name of the workspace, and of the root repo. It is in every URL. The default is the folder name of the main checkout. |
| `repos` | the root | The member repos: a name and a path for each one. A relative path starts at the main checkout of the root repo. A path can start with `~/`. |
| `stack.pattern` | the root | Optional. A regular expression: its first match in a branch name is the stack name. See "How it works". |
| `member_of` | a member | The path of the root repo, relative to the main checkout of the member. |

The link goes both ways. A member that the root does not list is an error, and a listed repo whose file does not name the root is an error. Each message names both files.
A listed repo with no `ilot.yaml` declares no app. A listed repo that is not cloned is left out.

Every repo can declare `env`, `external`, `apps` and `wrap`.

`wrap` is a prefix for every `build`, `run` and `stop` command of the repo of the file.
Use it when a tool manager gives the repo its tools, for example `wrap: mise exec --` or `wrap: nix develop -c`.
`ilot` then runs `<wrap> sh -c '<command>'`. `ilot` no longer looks for a `mise.toml` by itself.

The fields of an app:

| Field | Meaning |
|---|---|
| `run` | Command that starts the app, in `/bin/sh -c`. Required, except for a container. |
| `stop` | Command that stops the app. Without it, `ilot` sends SIGTERM. Use it when the `run` command starts something that a signal does not stop, for example `run: docker run --name db-{{.Stack}} ...` with `stop: docker stop db-{{.Stack}}`. Not for an app with a `container` block. |
| `build` | Command that builds the app, in `/bin/sh -c` as `run`. `ilot start` runs it in the checkout before it starts the app. |
| `env` | Environment variables of the process. |
| `dir` | Working folder of the process. A relative path is relative to the checkout. The default is the checkout. |
| `ports` | Names of the ports of the app. `ilot` gives each name a free port of its range and keeps it. A container app declares its ports in the `container` block. |
| `fixed_ports` | Fixed numbers for some of the ports, for example `{http: 9090}`. They apply to the `base` stack only. In another stack, `ilot` gives the port a number of its range. |
| `route` | Port that the proxy URL sends requests to. |
| `home_path` | Path that the root of the proxy URL redirects to, for example `/swagger/`. It needs a `route`. |
| `ready` | How `ilot` knows that the app is ready: `port`, and `path` for an HTTP check. Without `path`, `ilot` waits until the port accepts a connection. The default port is the `route` port. A `command` replaces the port check: `ilot` runs it until it exits with 0. The command can use templates, and it runs behind the `wrap` of its repo. |
| `depends_on` | Apps and external services that the app needs at startup. |
| `autostart` | `ilot boot` starts the app in the `base` stack. |
| `scope` | `base` or `stack`: which stack serves the app to the apps that depend on it. See "How it works". The old keys `shared: true` and `per_stack: true` are now `scope: base` and `scope: stack`. |
| `watch` | If the files of the app change, `ilot` restarts the app. List the files with `paths`, relative to the working folder, and file extensions in `exts`. It needs `watchexec` on the `PATH` of the app. A watcher process runs `ilot restart <app>` at each change, so an app with a `build` is built again first. If the build fails, the app keeps running and the error is in `ilot logs <app>-watch`. |
| `container` | Runs the app as a Docker container. See "Container apps". |

The `build`, `run`, `stop` and `env` values can use these templates:

| Template | Value |
|---|---|
| `{{.Ports.<name>}}` | The port of this app with that name. |
| `{{port "<app>" "<name>"}}` | The port of another app or of an external service. If the stack runs its own copy of the app, it gets the port of that copy. Else it gets the port of the `base` copy. The `scope` of the app can fix that choice. |
| `{{url "<app>"}}` | The proxy URL of an app, with the same rule. |
| `{{host "<app>"}}` | The host name that this app uses to reach another app: `localhost` for a process, and `host.docker.internal` for a container. |
| `{{repo "<name>"}}` | The checkout of another repo for this stack. |
| `{{.WorktreeDir}}` | The checkout of the repo of the app for this stack. |
| `{{.BinDir}}` | The folder for the binaries of the stack. |
| `{{.StackDir}}` | The folder for the state and the logs of the stack. |
| `{{.Stack}}` | The name of the stack. |
| `{{.Workspace}}` | The name of the workspace. |
| `{{.User}}` | Your user name on this machine. |
| `{{.Domain}}` | The domain of the proxy URLs, which the global file sets. |
| `{{.ProxyPort}}` | The port of the proxy. |

An app never chooses its own port. It reads the port from a flag or a variable that its `run` command gives it.

An external service is a service that `ilot` does not start. It has a `port` and a `start_hint`.
`ilot start` makes sure that its port answers. If the port does not answer, `ilot start` prints the `start_hint`.

### Container apps

An app with a `container` block runs as a Docker container named `ilot_<app>_<stack>_<workspace>`.
It needs no `run`. If the app has a `build`, it runs before the container starts.
Docker is needed only for these apps.

```yaml
apps:
  db:
    container:
      image: postgres:17
      ports: {pg: 5432}
      mounts: {"{{.StackDir}}/pg": /var/lib/postgresql/data}
      env_files: [.env.db]
      args: [--shm-size=1g]
    env:
      POSTGRES_PASSWORD: dev
    ready: {command: pg_isready -U postgres}
```

| Field | Meaning |
|---|---|
| `image` | Image to run. Required. |
| `ports` | The ports of the app, from the name of a port to its number inside the container. A container app has no top-level `ports`. |
| `mounts` | Folders to mount, from the path on this machine to the path in the container. The path on this machine can use templates. A relative path is a folder of the checkout of the repo. |
| `env_files` | Env files that `docker run` reads. |
| `args` | More flags for `docker run`, before the image. A flag can use templates. |

`ilot` gives each port of a container a port of the stack, as for any app, and publishes it on `127.0.0.1` only.
The proxy and the other apps therefore reach a container on `localhost`, and two stacks can run the same container.
Use `{{host "<app>"}}` and `{{port "<app>"}}` for the address of another app: a container gets `host.docker.internal`, and a process gets `localhost`.
`ilot` adds the `--add-host` flag that Docker on Linux needs for that name.
On Linux, that name is the address of the Docker bridge, and not the loopback address. A container there reaches only an app that listens on every interface. It does not reach an app that listens on `127.0.0.1` only, and it does not reach another container app, because `ilot` publishes a container on `127.0.0.1` only. Docker Desktop on macOS has no such limit.

The `env` of the app goes to the container with `-e KEY`, so the values stay out of the command line.
`ilot` pulls a missing image before any build, one time for all the apps that use it.
A `ready` check with a `command` runs inside the container, with `docker exec`.
Give a container app a `command` or a `path`: Docker accepts a connection on a published port before the server inside listens, so a bare port check passes at once. `ilot check` warns about such an app.
For a case that the block does not cover, write `run: docker run ...` with a `stop` command.

## The global file

The values of the machine, and the values that must stay out of a repo, go in `~/.config/ilot/config.yaml`.
The file is optional, and so is each of its keys. This example shows the defaults of `proxy`, `ports` and `shell_env`.
Make the file readable only by you (`chmod 600`) when it holds tokens.

```yaml
proxy:
  listen: 127.0.0.1:4580      # the address of the proxy: change it when another program has the port
  domain: localhost           # the end of every URL
ports:
  first: 20000                # the range that ilot gives ports from
  last: 29999
shell_env:
  args: [-l]                  # the flags of the shell that gives the base environment
workspaces:
  shop:                       # the name of a workspace
    repos:
      web: ~/elsewhere/web    # a member repo that is cloned at another path
    env:
      LOG_LEVEL: debug        # every app of the workspace
    apps:
      front:
        API_TOKEN: <token>    # one app only
```

- With another `domain`, for example `localtest.me`, every name under it must point to this machine.
- A fixed port of an `ilot.yaml` must be outside the `ports` range.
- A path under `repos` replaces the path that the root repo declares. The `ilot.yaml` of that clone still names the root with `member_of`.
- After a change of `proxy`, run `ilot restart` with no app, which starts a new daemon.

An older `ilot` read a personal file, `~/.config/ilot/env.yaml`. `ilot` now stops with an error while that file exists: move its content under `workspaces.<name>` of the global file, then delete it.

`ilot` builds the environment of an app in this order, and the last value wins:

1. The shell. `ilot` runs your shell, `$SHELL`, with the flags of `shell_env.args`, and takes its whole environment. The default, `-l`, makes a login shell. The shell starts with `HOME`, `USER`, `LOGNAME`, `SHELL`, a system `PATH`, `LANG`, `LC_ALL`, `TMPDIR` and `SSH_AUTH_SOCK` only, so a value that you export in a terminal never reaches an app.
   `LANG`, `LC_ALL`, `TMPDIR` and `SSH_AUTH_SOCK` come from the terminal, unless a profile file sets them. `ilot env` shows them as `session`. An app gets them, but a change of them does not make its specification change, so a command from another terminal restarts nothing.
2. The top-level `env` of the `ilot.yaml` of the root repo, for the apps of every repo.
3. The top-level `env` of the `ilot.yaml` of the repo of the app, then the `env` of the app.
4. The global file: the `env` of the workspace, then its entry under `apps`.
5. The values that an `ilot.yaml` writes with a template. The global file cannot replace them. A key takes its value from the most specific `ilot.yaml` layer first, so a literal value of the app replaces a template of the root repo.

A login shell reads the profile files, for example `~/.zprofile` or `~/.profile`. It does not read the files of an interactive shell, for example `~/.zshrc`.
If a tool is on your `PATH` in a terminal and an app does not find it, look at the `PATH` that `ilot env <app>` prints. Then move the line that adds the tool to a profile file, or give the shell another flag with `shell_env.args`, for example `[-l, -i]`.

A container app does not get the `env` of the workspace in the global file, so a third-party image does not get your tokens. It gets its entry under `apps`.
An app gets its whole environment from `ilot` when it starts, and nothing from the daemon. To give a running app fresh values, run `ilot start <app>` again.

## For AI agents

`ilot` ships a skill for Claude Code, `skill/SKILL.md`.
It tells an agent that `ilot` is its own tool, and it describes every command, the stacks and the traps.
`ilot skill install` copies it to `~/.claude/skills/ilot/SKILL.md`, and `ilot skill print` prints it.
Run `ilot skill install` again after you upgrade `ilot`.
The skill pre-approves every `ilot` command.
When you add a command, add it to the skill too: a test fails otherwise.

## Files and folders

| Path | Content |
|---|---|
| `~/.config/ilot/config.yaml` | The global file. |
| `~/.local/state/ilot/state.json`, `state.json.bak` | The state of every workspace: the stacks, their bindings, their apps and their ports. The backup is the version before the last change. |
| `~/.local/state/ilot/state.lock` | The lock that one `ilot` at a time holds to read and change the state. A command waits 10 seconds for it at most. |
| `~/.local/state/ilot/workspaces/<workspace>/<stack>/` | The binaries of a stack in `bin/` and its logs in `logs/<app>.log`. |
| `~/.local/state/ilot/daemon.lock`, `daemon.sock`, `daemon.log`, `processes.json` | The lock, the socket and the log of the daemon, and the record of the processes that it runs. If the path of the socket is longer than 100 bytes, the socket is in `/tmp/ilot-<user ID>/`. |

A log rotates at 10 MB, and `ilot` keeps the 2 previous files.
If another program has port 4580, each command prints a warning, the apps run, and their URLs do not answer.
Set `proxy.listen` to another address in the global file, for example `127.0.0.1:4581`, then run `ilot restart`.
After an upgrade of `ilot`, the old daemon keeps running, and each command warns about it. Run `ilot restart` to replace it.
After you delete the worktrees of a stack, run `ilot clean` to give back its ports. `ilot clean` also removes a stack that lost its last worktree to `ilot stack bind`. It names each app that it stops. A worktree that you moved with `git worktree move` keeps its stack: `ilot clean` binds the stack to the new folder when one unbound worktree of the repo has a branch that gives the name of the stack.

## Development

```bash
go vet ./...
go test ./...
go test -tags e2e ./...
```

The `e2e` tag adds the end-to-end test. It builds `ilot`, makes a home folder under `/tmp` with the workspace of `testdata/e2e`, and runs the real commands: a start in the main checkout and in a worktree, requests through the proxy, a kill of the daemon, and a restore.
It needs git and the Go toolchain. Its home folder has its own state folder and its own global file, which gives the proxy a free port.
