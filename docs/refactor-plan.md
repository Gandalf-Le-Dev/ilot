# Refactor plan: ilot as a personal tool

Status: proposal, written on 2026-11-19 against commit `9adada9`. No code changed.

This plan turns `ilot` from the tool of one company into a tool that one person installs and uses on a laptop.
It gives the decisions with their reasons, the target architecture, the ordered steps, and the questions that only the owner can answer.

## 1. Goal

After the refactor, this sequence works on a fresh macOS or Linux machine, with git as the only requirement:

```bash
go install github.com/Gandalf-Le-Dev/ilot@latest
cd ~/code/shop            # a repo with an ilot.yaml at its root
ilot start api            # builds, starts, prints http://api.base.shop.localhost:4580
git worktree add ../shop-retry -b retry-jobs && cd ../shop-retry
ilot start api            # a second copy: http://api.retry-jobs.shop.localhost:4580
```

The product keeps one idea: a worktree runs the apps that you start in it and borrows the others from the base stack.
A stack is the set of apps that run for one line of work.
The base stack is the stack of the main checkouts.
Everything that does not serve that idea is deleted or replaced by something smaller.

## 2. What I tested in the critique

I read every non-test file and compared it with the critique. I also ran the tools on this Mac (macOS 27, Go 1.27.3).

| Claim | Result |
|---|---|
| D1, a failed `IsUp` deletes a live socket | True, see `processcompose.go:268-286`. The loop in `ilotctl.go:217-229` never reads `ctx.Err()`, so each remaining app starts one more daemon. |
| D2, one broken stack blocks all stacks | True. `prepareStacks` loads the configuration of every saved stack and joins the errors (`stackconfig.go:93-96`). |
| D3, the stack name follows the branch | True, see `stack.go:76` and `checkouts.go:53`. |
| D4, the global lock covers builds and waits | True. `Start` holds `Store.Lock` from the first line to the last (`ilotctl.go:79-83`). `buildApps` does not take `build.lock`. |
| D5, a dead daemon makes any command start everything | True, see `files.go:30-44`. |
| D6 and D7, route files and name clashes | True in the code. The Traefik behavior stays "not run". Both disappear with the route files (decision 2). |
| T1, tests fail on macOS | True: 12 failures in 3 packages, all `/var` against `/private/var`. |
| The `Controller` mixes the configurations of several stacks | True, see `stackconfig.go:54-65` and `plan.go:33-39`. |

Facts that the critique does not have, and that change the design:

- This Mac has no `process-compose`, no `watchexec`, no `flock` command and no Traefik. macOS ships no `flock` command at all, so the watcher of today cannot work here.
- `getaddrinfo` resolves `api.main.localhost` to `127.0.0.1` and `::1` on this Mac, with no DNS setup. A name under `.test` does not resolve.
- `ports.IsFreeOnHost` listens on every interface (`ports.go:29`). On macOS, that can open the firewall dialog for an unsigned binary. A probe on the loopback address does not.
- A unix socket path is limited to 104 bytes on macOS. `t.TempDir()` paths are longer than that, so the socket tests need a short folder.

Where I do not follow the critique:

- The critique suggests a thinner layer over process-compose, with one project per stack and `depends_on`. I recommend the opposite: remove process-compose (decision 3).
- The critique suggests about ten configuration keys for the company values. I delete most of them. A key for a feature that nobody uses on a laptop is dead weight (decision 1).
- The critique orders the work as patches to D1, D2, D4 and D5 first. I cut first and replace the supervisor layer second, because a patch to code that the next phase deletes is waste.
- The critique keeps a third stack for shared apps. I merge it into the base stack.

## 3. Decisions

### Decision 1: company couplings and the configuration shape

#### What goes, what stays

| Coupling | Where | Decision |
|---|---|---|
| `/etc/devhost.env` marker | `main.go:38,330` | Delete. |
| Devhost path translation | `workspace.go:100-110` | Delete. A mount names a path on this machine. |
| Ticket prefixes `PROJ-`, `OPS-`, `TASK-` | `stack.go:28` | Delete. Replace with an optional `stack.pattern` key. |
| `platform`, `~/src/platform`, `devbox/ilot.yaml` | `workspace.go:21-23`, `config.go:19` | Delete. `ilot` finds the workspace from the `ilot.yaml` of the current repo. |
| Stack names `master` and `devbox` | `stack.go:13-14` | Rename `master` to `base`. Merge `devbox` into `base`. |
| zsh and the allow-list of variables | `shellenv.go` | Replace. `ilot` reads the whole environment of a login shell of `$SHELL`. |
| `KEYRING_APP_KEY`, `APP_NAME`, `VSCODE_REPO_PATH` | `environment.go:109-116` | Delete. The `env` keys cover them. |
| `LC_DEVBOX_LOGIN` | `main.go:359,365` | Replace with the OS user name. |
| The app named `temporal` and `temporal_namespace` | `temporal.go`, `state.go:20` | Delete. No hook replaces it in the first release. |
| Traefik container `ilotproxy_<login>`, routes folder, `DEVHOST_PORT_7` | `main.go:376-402` | Delete. The daemon holds the proxy (decision 2). |
| Network `ilotproxy`, backend host `devbox`, `links`, `external_links` | `container.go` | Delete. The `container:` block stays in a smaller form (decision 2). |
| `gateway`, `gateway_aliases`, `gateway_base_file`, `services.json` | `files.go:69-108` | Delete. The `port` and `url` templates cover the need. |
| mise detection by `mise.toml` | `workspace.go:112` | Replace with a `wrap` key per repo, for example `wrap: mise exec --`. |
| Artifactory, GPG, npmrc (`ilot bootstrap`) | `internal/bootstrap` | Delete. |
| The launcher and its company Go proxy | `internal/launcher` | Delete. |
| Wildcard DNS for `<domain>` | README | Replace. The default domain is `localhost`, which needs no DNS. |

Only three things become configuration: the stack pattern, the proxy address with its domain, and the port range.
The other values had one user, and that user was the devbox.

#### The files

`ilot` reads two kinds of files.

The repo file is `ilot.yaml` at the root of a repo. Its name and its place are fixed.
A workspace is the set of repos that share stacks, ports and one name.
One repo of a workspace is the root repo. Its file can carry `name`, `repos` and `stack`.
A member repo names the root with `member_of`. The link goes both ways so that `ilot` needs no registry and no history to find the workspace.

```yaml
# ~/code/shop/ilot.yaml, the root repo
name: shop                      # default: the folder name of the main checkout
repos:
  web: ../web                   # relative to the main checkout of this repo
stack:
  pattern: '[A-Za-z]+-[0-9]+'   # optional: the first match in the branch name is the stack name
env:
  LOG_LEVEL: info
external:
  postgres:
    port: 5432
    start_hint: brew services start postgresql@17
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
  mailpit:
    run: mailpit --smtp 127.0.0.1:{{.Ports.smtp}} --listen 127.0.0.1:{{.Ports.http}}
    ports: [smtp, http]
    route: http
    scope: base                 # one copy for every stack
```

```yaml
# ~/code/web/ilot.yaml, a member repo
member_of: ../shop
wrap: mise exec --              # prefix of every build and run command of this repo
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

The global file is `~/.config/ilot/config.yaml`. It is optional. It replaces the personal file `env.yaml`.
It holds the values of the machine and the values that must stay out of a repo.

```yaml
proxy:
  listen: 127.0.0.1:4580
  domain: localhost
ports:
  first: 20000
  last: 29999
shell_env:
  args: [-l]                    # flags of the shell that gives the base environment
workspaces:
  shop:
    repos:
      web: ~/elsewhere/web      # this repo is cloned at another path
    env:
      LOG_LEVEL: debug          # every app of the workspace
    apps:
      front:
        API_TOKEN: <token>      # one app only
```

The rules:

- `ilot` finds the main checkout of the repo of the current folder. It reads the `ilot.yaml` of that main checkout to learn the workspace.
- A stack reads `apps`, `env` and `external` from its own checkout of each repo. If that checkout has no `ilot.yaml`, the stack reads the file of the main checkout. An untracked `ilot.yaml` in the main checkout therefore works in a repo where you cannot commit one.
- A member whose `member_of` names a root that does not list it is an error. The message names both files.
- Two workspaces with the same name on one machine are an error. The message tells you to set `name`.
- The environment layers stay, in this order: the login shell, the root `env`, the repo `env`, the app `env`, the global file, then the values that a template computes.

#### App placement: one field

`shared` and `per_stack` become one field, `scope`.

| Value | Meaning |
|---|---|
| absent | If the stack runs its own copy, an app that depends on it gets that copy. If not, it gets the base copy. |
| `stack` | An app that depends on it always gets the copy of its own stack. This is `per_stack` of today. |
| `base` | One copy, in the base stack, for every stack. This is `shared` of today. Fixed ports are no longer required. |

Membership replaces the `Running` flag as the input of this rule.
A stack owns an app from `ilot start <app>` until `ilot stop <app>` or `ilot rm`.
A crash or a failed start does not change ownership. The retry hint in `ilotctl.go:134-147` therefore goes away.

#### Stack names without a compiled pattern

1. The main checkout of every repo is the `base` stack, on any branch.
2. A linked worktree gets its stack name one time, at the first `ilot start` in it. `ilot` stores the pair of worktree path and stack name in the state. This is the binding.
3. The default name is the slug of the branch name: lowercase letters, digits and dashes, 63 characters at most. With `stack.pattern`, the name is the first match in the branch name.
4. With a detached HEAD, the default name is the slug of the folder name.
5. `ilot stack bind <name>` sets or changes the binding by hand. Use it to put the worktree of a second repo in an existing stack.
6. After the binding, `git switch`, a rebase and a bisect do not move the worktree to another stack. This fixes D3.

The branch is the default because it is the natural key across repos: the same ticket usually gets the same branch name in each repo.
The folder name is often chosen by a tool.

### Decision 2: platforms, Docker and the proxy

Supported platforms: macOS and Linux. Windows is out of scope, because the design uses `flock`, process groups and unix sockets.

Docker is a requirement only for a workspace that declares a container app.
The owner asked to keep container apps, so the `container:` block stays in a smaller form:

```yaml
db:
  container:
    image: postgres:17
    ports: {pg: 5432}            # port name: port inside the container
    mounts: {"{{.StackDir}}/pg": /var/lib/postgresql/data}
    env_files: [.env.db]
    args: [--shm-size=1g]        # more flags for docker run
  env:
    POSTGRES_PASSWORD: dev
  ready: {port: pg}
```

- The planner turns the block into a `docker run --rm` command and a `docker stop` command. The daemon sees a normal process and knows nothing about Docker.
- `ilot` publishes each port on `127.0.0.1:<stack port>`. The proxy and the other apps reach the container on `localhost`, as for any app.
- The container name is `ilot-<app>-<stack>-<workspace>`. Before a start, `ilot` removes a leftover container of that name.
- The `host` template gives `localhost` to a process and `host.docker.internal` to a container. `ilot` adds the `--add-host` flag that Linux needs for that name.
- The network `ilotproxy`, `links`, `external_links` and the devhost path translation go away. The loopback ports replace them.
- For a case that the block does not cover, write `run: docker run ...` with the `stop` key.

The proxy is a Go reverse proxy inside the `ilot` daemon. Traefik goes away.

- The URL of an app is `http://<app>.<stack>.<workspace>.<domain>:<port>`, for example `http://api.retry-jobs.shop.localhost:4580`.
- The default domain is `localhost`. Browsers, curl and the macOS resolver send every name under `localhost` to the loopback address. I tested the resolver on this Mac.
- The proxy listens on `127.0.0.1:4580` by default. Both values are in the global file.
- A request for an unknown host gets a page that lists every route. This page replaces the `home.<domain>` page that `ilot ls` printed but never served.
- A request for an app that does not run gets a 503 page with the command that starts it.
- `net/http/httputil.ReverseProxy` covers WebSocket upgrades and streamed responses. HTTPS and gRPC over cleartext HTTP/2 are not in the first release.

Traefik in Docker is a bad fit for a laptop, for three reasons:

- Docker must run all day for routing alone.
- The file provider depends on file events across a virtual machine mount.
- The routes folder needs a lookup on each command.

A Traefik or Caddy binary is one more install with a pinned version, for about 150 lines of Go.
The in-process proxy also removes D6 and D7, because no route file and no router name exist.

The owner chose `localhost:4580` as the only address. Port 80 and a DNS answer for a custom domain are out of scope.
`proxy.domain` stays only for the Linux case below.
`proxy.listen` stays in the global file only so that you can move the proxy off a port that another program uses.

On Linux without systemd-resolved, a server-side call to `x.localhost` can fail. `ilot doctor` tests the resolution and names two fixes: another `domain` such as `localtest.me`, or the `port` template for calls between apps.

### Decision 3: replace process-compose with a small daemon in `ilot`

`ilot` uses five things of process-compose: a detached supervisor, log files with rotation, readiness probes, restart, and stop with a timeout.
It does not use `depends_on`, the TUI or the restart policy.

The integration costs more than those five things:

- It is the source of D1 and D5, and of the global reconcile where a command in one stack can restart apps in another stack.
- Every app inherits the environment of the daemon. That forces the recorded environment file and the `ilot restart` of everything to refresh one variable.
- process-compose kills an app that is not ready after 600 probes. `--timeout` does not change that.
- The contract is strings and a pinned version that `ilot` never reads.
- The proxy needs a long-lived process anyway. One daemon for both is simpler than a daemon that supervises a proxy.
- A second binary to install breaks the goal of section 1.

The daemon is `ilot daemon run`, the same binary. It is a supervisor and a proxy with no knowledge of stacks, configuration or git.
The CLI computes each process specification and sends it. This keeps the daemon small enough to test fully.

| Concern | Design |
|---|---|
| Proof of life | The daemon holds `flock` on `daemon.lock` for its whole life. A socket file alone proves nothing. This removes D1 by construction. |
| Start | The CLI starts the daemon on demand, detached, with its output in `daemon.log`. Two concurrent starts race on the lock, and one wins. |
| Processes | Each runs as `sh -c <command>` in its own process group, with exactly the environment that the CLI sent. |
| Logs | The daemon appends the raw output to `logs/<app>.log` and rotates at 10 MB, with 2 old files. `ilot logs` no longer unwraps JSON. |
| Readiness | A TCP connect, an HTTP GET or a command, one time per second. A probe never kills a process. |
| Stop | The optional `stop` command, or SIGTERM to the group. SIGKILL to the group after 20 seconds. |
| Crash of the daemon | The daemon records each process ID with its start time. A new daemon kills the survivors before it listens. |
| Upgrade | `GET /v1/status` gives the version. On a mismatch, the CLI prints `ilot daemon restart` and does not restart anything by itself. |
| Socket path | `<state>/daemon.sock`. If that path is longer than 100 bytes, the daemon uses `/tmp/ilot-<uid>/<hash>.sock`. |

The API, as HTTP over the unix socket:

| Call | Effect |
|---|---|
| `GET /v1/status` | Version, process ID, start time, proxy address. |
| `GET /v1/processes` | Name, state (`starting`, `ready`, `exited`, `stopping`), process ID, exit code and specification hash of each process. |
| `PUT /v1/processes/{name}` | Start the process. Same hash and alive: no change. Different hash or exited: replace. |
| `POST /v1/processes/{name}/restart` | Stop, then start with the stored specification. |
| `DELETE /v1/processes/{name}` | Stop and forget. |
| `PUT /v1/routes` | Replace the route table of the proxy. |
| `POST /v1/shutdown` | Stop every process, then exit. |

Because the calls are per process, a command touches only the processes that it names. No command renders the apps of another stack.

If you prefer to keep process-compose, the fallback has three parts:

- Talk to its REST API over the socket.
- Add the lock file as proof of life.
- Run the proxy as one supervised `ilot proxy` process.

Phase 2 and steps 3.1 and 3.2 change. The other phases stay. The cost is one required binary and the environment inheritance.

### Decision 4: target structure

Section 4 gives the packages, the state model and the locking.
The main rule: planning and rendering are pure functions over a snapshot, and one small executor does the I/O.

Defects that the restructure removes, and defects that need their own step:

| Defect | Fixed by | Step |
|---|---|---|
| D1 live socket deleted | Lock file as proof of life, typed client errors | 2.4 |
| D1 loop goes on after cancel | Own fix | 5.7 |
| D2 one broken stack blocks all | A command loads the configuration of its own stack and of base only. `stop`, `rm`, `ls` and `logs` need the state file only. | 5.4 |
| D2 corrupt state file | One file, written with `fsync` and rename, with a backup of the last version | 5.1 |
| D3 stack follows the branch | Binding | 4.6 |
| D4 lock over builds and waits | The executor holds the lock for load, plan and save only | 5.5 |
| D4 two build locks | One Go build lock per stack, used by `start`, `restart` and the watcher | 3.6 |
| D5 dead daemon starts everything | Calls per process. The planner reads the real status. | 5.4, 5.6 |
| D6 route files written in place | No route files | 3.2 |
| D7 name clashes | The instance name is `<app>.<stack>.<workspace>`. A label cannot hold a dot. | 5.1 |
| D8 stored ports not tested again | Own fix | 5.8 |
| D8 template points at an app started later | The planner sets the membership of the whole plan before it renders | 5.2 |
| D8 false warning of `ilot env` | The pure renderer prints nothing | 5.3 |
| D8 hidden limit of 10 minutes | Probes never kill | 2.2 |
| D8 no schema version | `"version": 1` | 5.1 |
| D8 ghost apps | `ilot ls` marks an app that no file declares. `ilot stop` removes it and frees its ports. | 5.8 |
| T1 tests fail on macOS | Canonical paths in the product and in the tests | 0.1 |
| No `log/slog` | The daemon uses `slog` from its first commit. The CLI gets an audit log. | 2.1, 6.4 |

### Decision 5: cuts and the first release

Cut:

- `ilot bootstrap` and `internal/bootstrap`: about 830 lines and 880 lines of tests.
- The launcher, `ilot rebuild` and `internal/launcher`: about 500 lines and 670 lines of tests.
- Temporal namespaces, the gateway file, the keyring, the devhost path translation.
- The container network `ilotproxy`, `links` and `external_links`.
- `internal/processcompose` and `internal/proxyfiles`.
- `ilot boot` and `autostart`. `ilot start` with no app name starts every app that the stack owns. `ilot start --all` does that for every stack.
- The skill install inside `boot`. It becomes `ilot skill install`.

Keep, with changes:

- The `container:` block, in the smaller form of decision 2, with the `host` template.
- The personal file, as the `workspaces` part of the global file.
- `external` services, `fixed_ports` for the base stack, `home_path`, `depends_on`, `ready`, `dir`.
- `watch`. The watcher process runs `watchexec ... -- ilot restart <app>`, and `ilot` takes the build lock in Go. `watchexec` is required only for an app with `watch`.

The commands of the first release:

```text
ilot start [apps...] [--no-deps] [--all]   converge: build and start what does not run. A running app stays as it is.
ilot restart <apps...>                     build, then restart the named apps
ilot stop <apps...> | --all                stop apps of this stack
ilot ls [--json]                           every stack: wanted and real status, ports, URL
ilot apps [--json]                         the declared apps of this stack
ilot logs [-f] [-n N] <app>
ilot env <app>
ilot check [path]
ilot rm <stack> / ilot clean [--dry-run]
ilot stack show / ilot stack bind <name>
ilot doctor
ilot daemon status | stop | restart
ilot skill print | install
```

`--workspace` and `--stack` select a stack from any folder. The watcher uses them.

Version 0.1.0 has three exit conditions:

- The sequence of section 1 works on macOS and on Linux in CI.
- The README describes only what exists.
- `go install` gives a working binary.

Distribution, as a suggestion that waits for the owner:

- The license is MIT. It is short, it matches the two dependencies, and it puts no duty on a user.
- GoReleaser builds the binaries for macOS and Linux, on arm64 and amd64, at each tag. They go on the GitHub release page.
- The same GoReleaser configuration publishes a Homebrew formula to a tap repo. The owner creates that repo and one access token.
- A Nix flake waits until a user asks for it, because `go install` and the binaries already cover Nix machines.

Not in the first release: HTTPS, hooks at stack creation and removal, a launchd or systemd unit, an automatic restart policy, a TUI, Podman, a Nix flake, Windows.

## 4. Target architecture

### Packages

| Package | Content | Comes from |
|---|---|---|
| `main` | The kong structs, the wiring and the printing. No domain logic. | `main.go`, minus `workspace.go`, `check.go:61-171`, `temporal.go`, `launch.go`, `bootstrap.go` |
| `internal/config` | The schema of `ilot.yaml` and of the global file: parse, merge, rules. | `appconfig`, `personal` |
| `internal/workspace` | Workspace discovery, the repos, one git runner with a cache per process, the binding, canonical paths. | `workspace.go`, `stack`, `checkouts` |
| `internal/state` | One state file with a version, the lock with a timeout, the port table. | `state`, `ports` |
| `internal/plan` | Pure: placement, start order, port needs, the process specification, the environment layers, the routes, the template test of `ilot check`. | Most methods of `ilotctl.Controller` |
| `internal/engine` | The executor: lock, load, plan, save, builds with the build lock, daemon calls, waits, progress lines. | The rest of `ilotctl` |
| `internal/daemon` | The supervisor, the proxy, the API server and its typed client. | New. Replaces `processcompose` and `proxyfiles`. |
| `internal/shellenv` | The environment of a login shell. | `shellenv` |
| `internal/logs` | Tail and follow of a raw log file. | `logs` |

`internal/plan` imports `config` and `state` types only. It runs no process, reads no file and opens no socket.
`ilot check` and `ilot env` call it directly. The `Controller` with 15 nil fields in `check.go:37-65` goes away.

### The data flow of `ilot start api`

1. `workspace` finds the workspace and the stack of the current folder: one `git` process, then the binding in the state.
2. `engine` takes the state lock. If another `ilot` holds it, `engine` prints who, and gives up after 10 seconds.
3. `engine` builds the snapshot: the state, the configuration of the own stack and of base, and the process list of the daemon.
4. `plan.Start` returns the ordered steps, the ports that are needed, and the new state. All the apps of the plan are members before any template renders.
5. `engine` gives the missing ports a number, tests the stored ports of the apps to start, saves the state, and releases the lock. The lock was held for milliseconds.
6. `engine` builds under the build lock of the stack, with the output on the terminal.
7. For each step in order, `engine` sends the specification to the daemon and waits until the process is ready. It stops at a cancel.
8. `engine` renders the running apps of the same stack again. It restarts each app whose specification hash changed, and says so.
9. `engine` sends the route table, which it derives from the state alone.

A change in a ticket stack can change only the specifications of that stack. Step 8 is therefore bounded.

### The state file

One file, `~/.local/state/ilot/state.json`, for every workspace.

```json
{
  "version": 1,
  "workspaces": {
    "shop": {
      "root": "/Users/me/code/shop",
      "stacks": {
        "base": {
          "checkouts": {"shop": "/Users/me/code/shop", "web": "/Users/me/code/web"},
          "apps": {
            "api": {"wanted": true, "ports": {"http": 20000}, "route": {"port": 20000}}
          }
        },
        "retry-jobs": {
          "checkouts": {"shop": "/Users/me/code/shop-retry"},
          "apps": {
            "api": {"wanted": true, "ports": {"http": 20001}, "route": {"port": 20001}}
          }
        }
      }
    }
  }
}
```

- `wanted` is the membership: the stack owns the app and the app must run. The real status comes from the daemon and is never stored.
- `checkouts` of a ticket stack is the binding. It holds the bound worktrees only. For another repo, the stack uses the main checkout.
- `route` is stored so that the route table, `ilot ls`, `ilot stop` and `ilot rm` need no `ilot.yaml`.
- One file makes the port table atomic. `Save` writes a temporary file, calls `fsync`, keeps the last version as `state.json.bak`, and renames.
- An unreadable file gives one error that names the file and the backup.
- Logs and binaries stay in folders: `~/.local/state/ilot/workspaces/<workspace>/<stack>/logs` and `.../bin`.

`ilot ls` shows both sides: `ready`, `starting`, `exited (1)`, `down` for a wanted app that the daemon does not have, and `stopped`.
If wanted apps are down, `ilot ls` ends with the line that restores them: `ilot start --all`.

### Locks

| Lock | Scope | Held during |
|---|---|---|
| `state.lock` | The machine | Load, plan, save. Never a build, a wait or a daemon call. |
| `build.lock` per stack | One stack | One build. `start`, `restart` and the watcher take it. A waiter prints a line. |
| `daemon.lock` | The machine | The whole life of the daemon. |

### Logging

Human progress lines go to stderr through one printer type. Data goes to stdout.
The CLI also writes one `slog` JSON record per action to `~/.local/state/ilot/ilot.log`: the lock wait, each port, each state change, each daemon call.
Each record carries `cmd`, `workspace`, `stack`, `cwd` and `pid`. The daemon writes `slog` JSON to `daemon.log`. Both files rotate.

## 5. Migration steps

Work on the branch `refactor/personal`. The devbox version lives in a fork in the company repo, so this repo needs no tag for it.

Every commit builds, and `go vet ./... && go test ./...` passes on it. The "Proof" column gives what the commit adds to that.
Each phase is a unit that an implementer and a separate reviewer can finish in one loop.

### Phase 0: safety net

| # | Commit | Proof |
|---|---|---|
| 0.1 | `fix: compare canonical paths so that a symlinked folder is the same repo` | The 12 failed tests pass on macOS. A new test puts a repo behind a symlink. |
| 0.2 | `chore: run vet and tests on Linux and macOS in CI` | The workflow is green on both systems. The same commit adds the pre-commit hook against secrets. |

### Phase 1: cuts

Each commit removes one feature with its tests, its README lines and its skill lines. The proof of each is a search for the removed names that returns nothing.

| # | Commit | Removes |
|---|---|---|
| 1.1 | `refactor: remove ilot bootstrap, which shares nothing with stacks` | `bootstrap.go`, `internal/bootstrap` |
| 1.2 | `refactor: remove the launcher, because releases replace it` | `launch.go`, `internal/launcher`, `ilot rebuild`, `stampedSourceTree` |
| 1.3 | `refactor: remove Temporal namespaces` | `temporal.go`, `temporal_namespace`, `HasTemporalNamespace` |
| 1.4 | `refactor: remove the gateway file` | `gateway`, `gateway_aliases`, `gateway_base_file`, `services.json` |
| 1.5 | `feat: let an app declare its stop command` | Nothing. It adds `stop` for a container that the `container:` block does not cover. A test reads the stop command in the specification. |
| 1.6 | `refactor: remove the devhost path translation of mounts` | `hostPathFor`. A mount names a path on this machine. |
| 1.7 | `refactor: remove the keyring and the default variables` | `KEYRING_APP_KEY`, `APP_NAME`, `VSCODE_REPO_PATH` |
| 1.8 | `refactor: install the skill only on request` | The install in `boot`. Adds `ilot skill install` and `ilot skill print`. |

After phase 1, about 4,300 of the 12,700 lines are gone, and the tool still runs with process-compose and Traefik.

### Phase 2: the daemon, beside the old supervisor

These commits add `internal/daemon`. Nothing uses it yet.

| # | Commit | Proof |
|---|---|---|
| 2.1 | `feat: add a process supervisor` | Tests with real `sh` children: output in the log file, exit code, rotation, and a stop that also kills a grandchild. |
| 2.2 | `feat: add readiness probes that never kill a process` | Tests for TCP, HTTP and command probes. A process that is not ready after the wait still runs. |
| 2.3 | `feat: serve the supervisor over a unix socket` | A round trip of every call on a socket in a short folder. The client returns a distinct error for "daemon down". |
| 2.4 | `feat: start the daemon on demand, with a lock file as proof of life` | Three tests: a cancelled context starts nothing and removes nothing, two concurrent starts give one daemon, a stale socket is replaced. |
| 2.5 | `feat: kill the processes that a dead daemon left behind` | A test kills the daemon with SIGKILL, starts a new one, and finds no child. |
| 2.6 | `feat: add the reverse proxy to the daemon` | Tests with `httptest` backends: routing by host, the list page, the 503 page, `home_path`, a WebSocket echo, a streamed response. |

### Phase 3: the switch

| # | Commit | Proof |
|---|---|---|
| 3.1 | `refactor: run the apps under the ilot daemon` | An adapter implements `ProcessSupervisor` with the client. `internal/processcompose` and the recorded environment are deleted. One contract test runs against the fake and against a real daemon. |
| 3.2 | `refactor: route through the daemon proxy` | `internal/proxyfiles`, `findRoutesDir` and `ILOT_ROUTES_DIR` are deleted. The default domain is `localhost`. |
| 3.3 | `refactor: run outside the devbox` | The marker test is deleted. The login comes from the OS user. |
| 3.4 | `refactor: read the environment from a login shell of $SHELL` | A test with a fake shell script. `ilot env` on this Mac shows the `PATH` of a login shell. |
| 3.5 | `feat: wrap the commands of a repo with a declared prefix` | `wrap` replaces the `mise.toml` detection. A test reads the prefix in the build and run commands. |
| 3.6 | `fix: one build lock for start and for the watcher` | The watcher runs `ilot restart`. A test holds the lock and sees the second build wait. No `flock` command remains. |
| 3.7 | `fix: probe ports on the loopback address` | A test with a listener on `127.0.0.1`. No firewall dialog on macOS. |
| 3.8 | `test: add an end-to-end test on a sample workspace` | Behind the build tag `e2e`: start an app in base and in a worktree, GET both through the proxy, kill the daemon, restore. CI runs it on both systems. |
| 3.9 | `refactor: publish container ports on the loopback address` | The network `ilotproxy`, `links` and `external_links` are deleted. `container.ports` and `container.args` exist. A pure test reads the `docker run` command. The end-to-end test starts one container on Linux, where CI has Docker. |

After phase 3, `ilot` runs on this Mac with git as the only requirement. Docker is needed only for a container app. Every later commit keeps the end-to-end test green.

### Phase 4: configuration and workspace

| # | Commit | Proof |
|---|---|---|
| 4.1 | `refactor: one git runner with a cache, and one worktree type` | `stack`, `checkouts` and `workspace.go` become `internal/workspace`. A test with a counting runner: `ilot start` in a workspace of 3 repos runs 5 git processes at most. |
| 4.2 | `feat: find the workspace from the ilot.yaml of the repo` | The `platform` constants are deleted. Tests: from the root, from a member, from a member worktree, a wrong `member_of`, a worktree with no file. |
| 4.3 | `feat: one global file replaces the personal file` | Tests for `proxy`, `ports` and `workspaces.<name>`. An old `env.yaml` gives an error that names the new file. |
| 4.4 | `refactor: name the base stack base, and place apps with scope` | `master`, `devbox`, `shared` and `per_stack` are gone. An old key gives an error that names the new key. |
| 4.5 | `feat: name a stack from its branch, with an optional pattern` | The compiled ticket pattern is deleted. Table tests for the slug, the pattern and the detached HEAD. |
| 4.6 | `fix: bind a worktree to its stack at the first start` | Tests: `git switch` after the binding keeps the stack, `ilot stack bind` moves it, `ilot clean` removes only a stack whose bound worktrees are all deleted. |

### Phase 5: state, planner, executor

| # | Commit | Proof |
|---|---|---|
| 5.1 | `refactor: one versioned state file, with the workspace in every name` | Tests: a wrong version, an unreadable file with its backup, the lock timeout with its message. URLs and process names carry the workspace. |
| 5.2 | `refactor: plan placement and start order in a pure package` | Table tests with no fake at all. Membership, not the process status, decides the serving stack. Each instance uses the configuration of its own stack. |
| 5.3 | `refactor: render specifications and environments in the pure package` | `ilot check` and `ilot env` build no `Controller`. `ilot env` prints no warning. |
| 5.4 | `refactor: call the daemon per process` | Three tests with a recording fake. A start in stack B sends no call for stack A. A broken `ilot.yaml` in A does not stop a start in B. `ilot stop` works with a broken file. |
| 5.5 | `fix: hold the state lock only to load, plan and save` | A test blocks a build in one stack and sees a start in another stack finish. |
| 5.6 | `fix: after a daemon restart, start only what the command needs, in order` | A test with an empty daemon and a state with wanted apps. `ilot ls` shows `down` and the restore line. `ilot start --all` restores in order. |
| 5.7 | `fix: stop the start loop at a cancel` | A test cancels after the first app and counts one daemon call. |
| 5.8 | `fix: test a stored port again before a start, and prune undeclared apps` | A test takes a stored port and reads the error with the port number. A test stops an undeclared app and finds its ports free. |
| 5.9 | `refactor: delete the Controller` | `internal/ilotctl` no longer exists. |

### Phase 6: commands, logs, release

| # | Commit | Proof |
|---|---|---|
| 6.1 | `feat: start converges, and restart rebuilds the named apps` | A second `ilot start api` sends no restart. `ilot boot` and the global `ilot restart` are deleted. `ilot daemon restart` exists. |
| 6.2 | `feat: add stop --all, clean --dry-run, --workspace and --stack` | Tests for each flag. `clean --dry-run` deletes nothing. |
| 6.3 | `feat: add ilot doctor` | It tests git, the state folder, the daemon version and the proxy address. It tests the resolution of `x.<domain>` and the shell environment. If an app has `watch`, it also tests `watchexec`. If an app has `container`, it also tests `docker`. |
| 6.4 | `feat: write an audit log with slog, and keep stdout for data` | `ilot ls --json`, `ilot apps --json` and `ilot env` give clean output with a warning present. A test reads the records of one `start`. |
| 6.5 | `docs: describe the personal tool in the README and in the skill` | The skill test passes. The skill pre-approves `ls`, `apps`, `logs`, `env`, `check`, `start`, `restart` and `stop` only. A sample workspace is in `examples/`. The `LICENSE` file is in the repo. |
| 6.6 | `chore: release v0.1.0 with GoReleaser` | `goreleaser release --snapshot` builds the four binaries. `go install github.com/Gandalf-Le-Dev/ilot@v0.1.0` on a clean machine, then the sequence of section 1. |

## 6. Risks

- The supervisor is new code in a domain with sharp edges: process groups, orphans, signals. The answer is the small scope of the daemon, tests with real child processes on both systems, and the end-to-end test.
- A login shell does not read the files of an interactive shell. A `PATH` entry that `.zshrc` adds is absent. `ilot env` shows it, `wrap` and `shell_env.args` fix it, and `ilot doctor` prints the `PATH` that the apps get.
- A daemon of an old version keeps running after an upgrade. The version test on each command catches it.
- A crash between two phases leaves no migration problem, because the new version does not read the old state. The old state belongs to the devbox.
- The plan deletes the gateway file and Temporal namespaces. If a setup of yours needs one of them, say so before phase 1.

## 7. Answers of the owner

The owner answered every question on 2026-11-27. No question is open.

- The devbox version lives in a fork in the company repo. This repo makes a clean break with it and adds no tag for it.
- The daemon inside `ilot` replaces process-compose and Traefik.
- Container apps stay. Decision 2 and step 3.9 give the smaller `container:` block.
- The URL shape is `<app>.<stack>.<workspace>.localhost:4580`. A custom domain and port 80 are not in the plan.
- The branch name is the default stack name.
- The distribution is the one of decision 5: MIT, GoReleaser binaries, a Homebrew tap, no Nix flake for now.
