<p align="center">
  <img src="docs/mark.svg" width="112" alt="The ilot mark: the o of ilot as a lagoon, half filled with water">
</p>

# îlot

Every git worktree gets its own island of apps.

`ilot` builds and starts the apps of each git worktree, gives each app its own ports, and puts it behind one local proxy.
You can work on two branches at the same time, and each one runs its own copy of the apps that it changes.
The apps that a branch does not change come from the `base` stack, which runs from the main checkout.

The name comes from the French word "îlot", a small island. The command and the repo spell it `ilot`.

## How it works

A stack is the set of apps that run for one line of work.
The main checkout of every repo is the `base` stack, on any branch.
`ilot` names the stack of a linked worktree after its branch: the branch name in lowercase letters, digits and dashes, 63 characters at most.
For example, the branch `feature/retry-jobs` belongs to the stack `feature-retry-jobs`.
The branch is the name because one line of work usually gets the same branch name in each repo.
With `stack.pattern` in the `ilot.yaml` of the root repo, the name is the first match of that regular expression in the branch name. With `pattern: '[A-Za-z]+-[0-9]+'`, the branch `feature/SHOP-12-cart` belongs to the stack `shop-12`. A branch that the pattern does not match keeps its whole name.
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

- macOS or Linux, on amd64 or arm64.
- git.
- Go 1.26, only to install `ilot` with `go install`.
- Docker, for container apps only. A mount of a container app names a path on this machine.
- watchexec, for an app with `watch` only.

`ilot doctor` tests each of them on your machine.

## Install

With Go:

```bash
go install github.com/Gandalf-Le-Dev/ilot@latest
```

The binary goes to `$(go env GOPATH)/bin`. Make sure that this folder is on your `PATH`.

Without Go, download the archive of your system from the [releases page](https://github.com/Gandalf-Le-Dev/ilot/releases), and put the `ilot` binary of the archive in a folder of your `PATH`.
With Homebrew, once the tap `Gandalf-Le-Dev/homebrew-tap` exists:

```bash
brew install --cask Gandalf-Le-Dev/tap/ilot
```

After an upgrade, run `ilot daemon restart` to replace the daemon of the old version, and `ilot skill install` to replace the skill.

## Try the sample workspace

`examples/` in this repo holds a workspace of two repos: `shop`, the root repo, with an `api`, and `web`, a member repo, with a `front` that calls the `api`. Both apps are small Go servers, so the sample needs Go on the `PATH` of a login shell.
The commands copy the two folders to `~/code` and make each one a git repo.

```bash
git clone https://github.com/Gandalf-Le-Dev/ilot && cd ilot
mkdir -p ~/code
cp -R examples/shop examples/web ~/code/
for repo in shop web; do (cd ~/code/$repo && git init -q && git add -A && git -c user.name=sample -c user.email=sample@localhost commit -qm sample); done
cd ~/code/web
ilot start front              # builds and starts api in base, then front
curl http://front.base.shop.localhost:4580
```

To work on a branch, add a worktree and start the apps that you change in it.
The `ilot.yaml` of `shop` sets `stack.pattern`, so the branch `feature/SHOP-12-cart` belongs to the stack `shop-12`:

```bash
git -C ~/code/shop worktree add ~/code/shop-cart -b feature/SHOP-12-cart
cd ~/code/shop-cart
ilot start api front
curl http://front.shop-12.shop.localhost:4580
ilot ls
```

`api` now runs twice: in `base` from the main checkout, and in `shop-12` from the worktree.
`front` runs in `shop-12` from the main checkout of the web repo, and its `API_URL` points to the `api` of `shop-12`.

## Set up your own repos

The files below are an example to adapt, not a sample that runs: they describe a Go API that needs a Postgres database, and an npm front that a tool manager, mise, gives its tools.
Replace the commands, the paths and the services with the ones of your repos.

1. Choose the root repo of the workspace, for example `~/code/shop`, and its member repos, for example `~/code/web`.
2. Write `ilot.yaml` at the root of the root repo. This file also lists the other repos:

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

3. Write `ilot.yaml` at the root of each member repo. It names the root:

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

4. Run `ilot check` in each repo. It merges the file with the files of the other repos and renders every template. It warns about an entry under `workspaces.<name>.apps` of the global file that names an app that no `ilot.yaml` of the stack declares.
5. Run `ilot start api` in the root repo. `ilot` builds `api`, starts it, and waits until `/health` answers.
6. Run `ilot ls` to see the status and the URL of each app.

## Commands

Every command finds the workspace from the repo of the folder that you are in, so run `ilot` inside a repo of the workspace.
`ilot start`, `stop`, `restart`, `logs`, `apps`, `env` and `stack` act on the stack of that folder.
`ilot ls`, `rm` and `clean` act on every stack.
`--workspace <name>` selects a workspace from any folder, and `--stack <name>` selects a stack of it. `ilot ls`, `rm`, `clean` and `doctor` take `--workspace`. `ilot start`, `stop`, `restart`, `logs`, `apps` and `env` take both; with `--workspace` and no `--stack`, they act on `base`. The workspace must have had one `ilot start`, which records its root repo, and the stack must exist.
`ilot stack show` and `ilot stack bind` take neither: they work on the worktree of the current folder only.

```bash
ilot ls                       # every app of every stack: status, ports, URL; then the worktree of each stack other than base
ilot ls --json                # the same as one JSON object
ilot apps                     # every app and external service that the ilot.yaml files of this stack declare, with the dependencies of each app
ilot apps --json              # the same as one JSON object
ilot start api                # build, then start the app after its dependencies; an app that runs stays as it is
ilot start                    # start every app that this stack owns and that does not run
ilot start api --no-deps      # start only this app
ilot start --all              # start every app that a stack owns and that does not run, for example after the daemon crashed
ilot stop api                 # stop the app in this stack
ilot stop --all               # stop every app that this stack owns; the apps of base that it used keep running
ilot logs api                 # the last 100 lines of the log of the app; a JSON line becomes one readable line
ilot logs --json api          # the lines of the app as it wrote them
ilot logs -f -n 20 api        # the last 20 lines, then the new ones
ilot env api                  # the environment of the app, with the source of each value
ilot check                    # check the ilot.yaml of this repo with the files of the other repos
ilot check path/to/ilot.yaml  # check that file
ilot rm shop-12               # stop a stack and delete its state, its logs and its routes
ilot clean                    # ilot rm on each stack whose bound worktrees are all deleted
ilot clean --dry-run          # print what ilot clean would do, and change nothing
ilot ls --workspace shop      # the same from any folder; rm, clean and doctor take --workspace too
ilot logs --workspace shop --stack shop-12 api  # the log of api in the stack shop-12, from any folder
ilot stack show               # the stack of this worktree, and the checkout of each repo for it
ilot stack bind shop-12       # put this worktree in the stack shop-12
ilot restart api              # build the app again, then restart it; its dependencies stay as they are
ilot doctor                   # test git, the state folder, the daemon, the proxy address, the name lookup, the login shell, and watchexec and docker when an app needs them
ilot daemon status            # the version, the process ID and the proxy address of the daemon
ilot daemon stop              # stop every app of every stack, then the daemon; the stacks keep their apps
ilot daemon restart           # stop every app and the daemon, start a new daemon, then start the apps that the stacks of every workspace own
ilot --version
ilot skill print              # print the skill for AI agents
ilot skill install            # copy the skill to ~/.claude/skills/ilot/SKILL.md
```

`ilot ls` gives each app one of these statuses: `starting`, `ready`, `stopping`, `exited (<exit code>)`, `down` for an app that its stack owns and that the daemon does not hold, for example after a crash of the daemon, or `stopped` for an app that no stack owns.
An app that no `ilot.yaml` of its stack declares any more is marked `undeclared`. It keeps its ports until `ilot stop <app>`, in its stack, removes it from the stack.
Before it starts an app, `ilot start` tests the ports that the app already has. If another program took one, the error names the port.
When an app is `down`, `ilot ls` ends with the command that starts it again: `ilot start --all`. It starts each app that a stack owns and that does not run, after its dependencies, and leaves the others as they are. It skips a stack whose bound worktrees are all deleted, with a warning: `ilot clean` removes that stack.

`ilot start` starts the missing dependencies first, and waits until each one is ready.
`ilot start` converges: it builds and starts what does not run, and an app that runs stays as it is. If the specification of a named app changed, for example after an edit of its `ilot.yaml`, `ilot start` builds it and replaces it.
`ilot restart <app>` builds an app again and restarts it, even with the same specification. Use it after a code change.
The app must run, or have exited: `ilot restart` refuses an app that is `down`, for example after `ilot daemon stop`, because it starts no dependency. Run `ilot start <app>` instead.
`ilot start` with no app starts every app that the stack of the folder owns and that does not run.
A command calls the daemon only for the apps that it starts, restarts or stops. A dependency that runs gets no call, and an app of another stack never restarts.
When the start changes a template of a running app of the same stack, for example because the stack now runs its own copy of a dependency, `ilot start` restarts that app and prints one line for it.
Only one build runs in a stack at a time. A second build, from another `ilot` command or from a watcher, waits and says so.
The stack owns every app of a start from the first line: an app that ends, or that fails to start, stays in the stack. `ilot ls` shows it `exited`, its log stays, and the next `ilot start` starts it again in the same stack. If another ilot command stops an app that you asked for during the start, or a dependency of it, `ilot start` still starts the others and prints their URLs and a line for each skipped app. Then it exits with a non-zero code, and the error names each app that did not start and why.
When the timeout passes while the app still runs, `ilot` keeps the app and says so.
`ilot stop`, `ilot rm`, `ilot ls` and `ilot logs` need the state only, so they work when an `ilot.yaml` is broken. A start reads the files of its own stack and of `base` only.

### How an app runs and stops

The daemon runs the `run` command of an app in `/bin/sh -c`, in a process group of its own.
The command must stay in the foreground. When its shell exits, the daemon kills what is left in its process group, so a `run` that starts a server with `&` and exits, or a tool that forks to the background, ends at once. `ilot ls` then shows the app `exited`. Write `exec <server>`, or use the flag of the tool that keeps it in the foreground.

A stop runs the `stop` command of the app, if it has one, for 20 seconds at most. If the app has no `stop` command, or if that command fails, the app gets SIGTERM. 20 seconds after that, whatever is left of its process group gets SIGKILL.
A stop can therefore take up to 40 seconds, twice the stop timeout.

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
`ilot` then runs `<wrap> sh -c '<command>'`.

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
| `scope` | `base` or `stack`: which stack serves the app to the apps that depend on it. See "How it works". |
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

The values of the machine, and the values that must stay out of a repo, go in `~/.config/ilot/config.yaml`, or in `$XDG_CONFIG_HOME/ilot/config.yaml` when that variable is set.
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
- After a change of `proxy`, run `ilot daemon restart`, which starts a new daemon.

An older `ilot` read a personal file, `~/.config/ilot/env.yaml`. `ilot` now stops with an error while that file exists: move its content under `workspaces.<name>` of the global file, then delete it.

`ilot` builds the environment of an app in this order, and the last value wins:

1. The shell. `ilot` runs your shell, `$SHELL`, with the flags of `shell_env.args`, and takes its whole environment. The default, `-l`, makes a login shell. The shell starts with `HOME`, `USER`, `LOGNAME`, `SHELL`, a system `PATH`, `LANG`, `LC_ALL`, `TMPDIR` and `SSH_AUTH_SOCK` only, so a value that you export in a terminal never reaches an app.
   `LANG`, `LC_ALL`, `TMPDIR` and `SSH_AUTH_SOCK` come from the terminal, unless a profile file sets them. `ilot env` shows them as `session`. An app gets them, but a change of them does not make its specification change, so a command from another terminal restarts nothing. An app that `ilot start` starts, or that you name in `ilot restart`, gets the values of your terminal. After an SSH reconnect, run `ilot restart <app>`.
2. The top-level `env` of the `ilot.yaml` of the root repo, for the apps of every repo.
3. The top-level `env` of the `ilot.yaml` of the repo of the app, then the `env` of the app.
4. The global file: the `env` of the workspace, then its entry under `apps`.
5. The values that an `ilot.yaml` writes with a template. The global file cannot replace them. A key takes its value from the most specific `ilot.yaml` layer first, so a literal value of the app replaces a template of the root repo.

A login shell reads the profile files, for example `~/.zprofile` or `~/.profile`. It does not read the files of an interactive shell, for example `~/.zshrc`.
If a tool is on your `PATH` in a terminal and an app does not find it, look at the `PATH` that `ilot env <app>` prints. Then move the line that adds the tool to a profile file, or give the shell another flag with `shell_env.args`, for example `[-l, -i]`.

A container app does not get the `env` of the workspace in the global file, so a third-party image does not get your tokens. It gets its entry under `apps`.
An app gets its whole environment from `ilot` when it starts, and nothing from the daemon. To give a running app fresh values, run `ilot restart <app>`.

## For AI agents

`ilot` ships a skill for Claude Code, `skill/SKILL.md`.
It tells an agent that `ilot` is its own tool, and it describes every command, the stacks and the traps.
`ilot skill install` copies it to `~/.claude/skills/ilot/SKILL.md`, and `ilot skill print` prints it.
Run `ilot skill install` again after you upgrade `ilot`.
The skill pre-approves `ilot ls`, `apps`, `logs`, `env`, `check`, `start`, `restart` and `stop`. Every other command, for example `ilot rm`, `clean` or `daemon restart`, asks you for permission first.
When you add a command, add it to the skill too: a test fails otherwise.

## Files and folders

| Path | Content |
|---|---|
| `~/.config/ilot/config.yaml` | The global file. |
| `~/.local/state/ilot/state.json`, `state.json.bak` | The state of every workspace: the stacks, their bindings, their apps and their ports. The backup is the version before the last change. |
| `~/.local/state/ilot/state.lock` | The lock that one `ilot` at a time holds to read and change the state. A command waits 10 seconds for it at most. |
| `~/.local/state/ilot/workspaces/<workspace>/<stack>/` | The binaries of a stack in `bin/` and its logs in `logs/<app>.log`. |
| `~/.local/state/ilot/ilot.log` | The audit log of the commands: one JSON record for each lock wait, port, change of the state and call to the daemon, with the command, the workspace, the stack, the folder and the process ID. |
| `~/.local/state/ilot/daemon.lock`, `daemon.sock`, `daemon.log`, `processes.json` | The lock, the socket and the log of the daemon, and the record of the processes that it runs. If the path of the socket is longer than 100 bytes, the socket is in `/tmp/ilot-<user ID>/`. |

Two variables move these folders. With `XDG_CONFIG_HOME`, the global file is `$XDG_CONFIG_HOME/ilot/config.yaml`. With `XDG_STATE_HOME`, every path under `~/.local/state/ilot/` is under `$XDG_STATE_HOME/ilot/`.

A log rotates at 10 MB, and `ilot` keeps the 2 previous files.
Every command prints its progress lines and its warnings on stderr, and only its data on stdout: the tables, the JSON of `--json`, the lines of `ilot env` and the URLs of `ilot start`.
If another program has port 4580, each command prints a warning, the apps run, and their URLs do not answer.
Set `proxy.listen` to another address in the global file, for example `127.0.0.1:4581`, then run `ilot daemon restart`.
After an upgrade of `ilot`, the old daemon keeps running, and each command warns about it. Run `ilot daemon restart` to replace it.
After you delete the worktrees of a stack, run `ilot clean` to give back its ports. `ilot clean` also removes a stack that lost its last worktree to `ilot stack bind`. It names each app that it stops. A worktree that you moved with `git worktree move` keeps its stack: `ilot clean` binds the stack to the new folder when one unbound worktree of the repo has a branch that gives the name of the stack.

## License

MIT. See `LICENSE`.

## Development

```bash
go vet ./...
go test ./...
go test -tags e2e ./...
```

The `e2e` tag adds the end-to-end test. It builds `ilot`, makes a home folder under `/tmp` with the workspace of `testdata/e2e`, and runs the real commands: a start in the main checkout and in a worktree, requests through the proxy, a kill of the daemon, and a restore.
It needs git and the Go toolchain. Its home folder has its own state folder and its own global file, which gives the proxy a free port.

### Release

A tag `v*` runs `.github/workflows/release.yml`. It first runs the checks of `ci.yml` on Linux and macOS, and stops when the secret `HOMEBREW_TAP_GITHUB_TOKEN` is empty. Then it runs GoReleaser with `.goreleaser.yaml`.
GoReleaser builds `ilot` for macOS and Linux on amd64 and arm64, stamps the version of the tag into `ilot --version`, puts the archives and their checksums on the release page, and pushes a Homebrew cask to the tap repo `Gandalf-Le-Dev/homebrew-tap`.
The workflow needs the repository secret `HOMEBREW_TAP_GITHUB_TOKEN`, a token that can push to the tap repo.

```bash
goreleaser check                       # test the configuration
goreleaser release --snapshot --clean  # build every archive in dist/, and publish nothing
git tag v0.1.0 && git push origin v0.1.0
```
