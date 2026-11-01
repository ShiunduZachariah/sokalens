# Self-hosted Actions runners

GitHub-hosted minutes are metered and reset monthly. Self-hosted runners
consume **none** of them. This is how the pipeline moves off metered runners
without giving up a single gate, and without running tests on the box that
serves users.

## The shape

Two runners, deliberately separate:

| Runner | Label | Runs | Why there |
|---|---|---|---|
| Workstation (WSL2 + Docker) | `local-ci` | every test and gate job | 8 CPUs and 32 GB idle most of the day; a failing test costs nothing |
| Production VPS | `vps-deploy` | the `deploy` job only | it is where the build and release already happen |

Both report to GitHub, so **GitHub remains the source of truth**: every deploy
is still triggered by a push to `main`, recorded as a workflow run with logs
and a commit SHA, and gated on the full matrix.

## Why tests do not run on the VPS

This is not only about CPU contention. The `api` job starts a PostgreSQL
service container bound to host port **5433** — and `ci-deploy.sh` reaches
production PostgreSQL at `127.0.0.1:5433` on that same host. On the VPS the
service container would fail to bind, and the job runs `task db:migrate` and
`task db:seed`. A seed job pointed at production is not a risk worth carrying
for a few saved minutes.

Keeping the test jobs on the workstation removes the collision entirely.

## What does not change: the gates

Moving a job changes **where** it executes, not what must pass:

- `deploy` still declares
  `needs: [detect, api, security-invariants, seo-invariants, security-audit,
  security-audit-go, secret-scan, worker, ops-tooling, web, mobile,
  contracts, infra-docs]`, so it cannot start until every gate finishes.
- The one deliberate exception is `admin`. It runs and reports, but is absent
  from that list, so `apps/admin` cannot hold back an `apps/web` or API
  release while it is off the active release path. Put it back in `needs`
  when admin ships again.
- The `Verify all gates green` step still fails the deploy on any result that
  is not `success` or `skipped`.
- `ci-deploy.sh` still validates the Caddyfile, captures a last-known-good
  image set, health-gates the release and rolls back on failure.
- The public-deployment and sitemap-indexability checks still run afterwards.

**Nothing deploys that has not passed the full matrix.** A different machine,
the same contract.

## Workstation runner (WSL2)

Run it in **WSL2 directly, not inside a container.** A containerised runner
using the host Docker socket cannot reach Actions *service containers* on its
own `localhost`, which is exactly what the `api` job needs for PostgreSQL.

```bash
wsl --install -d Ubuntu          # from PowerShell, if no general-purpose distro yet
```

Then inside Ubuntu (Docker Desktop → Settings → Resources → WSL integration
must be enabled for this distro):

```bash
mkdir -p ~/actions-runner && cd ~/actions-runner
curl -o runner.tar.gz -L https://github.com/actions/runner/releases/latest/download/actions-runner-linux-x64.tar.gz
tar xzf runner.tar.gz
./config.sh --url https://github.com/ShiunduZachariah/sports-prediction-platform \
            --token <REGISTRATION_TOKEN> --labels local-ci --name workstation
sudo ./svc.sh install && sudo ./svc.sh start
```

The token comes from Settings → Actions → Runners → New self-hosted runner, and
expires in an hour.

### The toolset a hosted runner gave you for free

`ubuntu-latest` ships a large image. A bare distro does not, and every gap
appears as a job failing on something that "should" exist. These are the ones
this pipeline actually needed:

```bash
apt-get install -y docker.io docker-compose-v2 docker-buildx build-essential \
                   postgresql-client curl tar git ca-certificates jq \
                   unzip zip file rsync libicu78
printf '{\n  "features": { "buildkit": true }\n}\n' > /etc/docker/daemon.json
systemctl enable --now docker
```

- **docker-compose-v2** is a separate package from `docker.io`. Without it
  `docker compose` is not a subcommand at all, and `infra-docs` fails with
  `unknown shorthand flag: 'f' in -f`.
- **docker-buildx**, plus BuildKit enabled in `daemon.json`. This one fails
  *quietly*: without buildx, `docker build` falls back to the legacy builder,
  which cannot parse `--mount=type=cache` and dies mid-build. The tell is
  `Sending build context to Docker daemon` and `Step 1/13` in the log —
  BuildKit prints neither. Hosted runners ship buildx and default to BuildKit,
  which is why the cache mounts added in #98 worked there and broke on the
  first self-hosted run that built ops-controller.
- **libicu** is the .NET dependency the runner itself needs. The package name
  tracks the Ubuntu release (`libicu78` on 26.04).
- **build-essential** provides the gcc the Go race detector needs. The project
  notes previously recorded `-race` as unrunnable on this workstation for
  exactly that reason; it now runs.
- **postgresql-client** is what `db:verify` shells out to.

The runner must not run as root, needs membership of the `docker` group, and
should **not** be given passwordless sudo. Nothing here requires it: the one
step that did — installing gitleaks into `/usr/local/bin` — now installs into
`RUNNER_TEMP`.

### Keeping the WSL distro alive

WSL reaps an idle distro and the runner service goes down with it. The symptom
is a runner that registers, reports online, and is offline minutes later with
no error anywhere. Something has to hold the distro open. A logon-triggered
scheduled task is tidier but needs elevation to register; a script in the
Startup folder does not:

`%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\sokalens-wsl-runner-keepalive.vbs`

```vbs
CreateObject("WScript.Shell").Run "wsl.exe -d Ubuntu -u root --exec /bin/sleep infinity", 0, False
```

Delete that file to stop the behaviour. **CI only runs while the workstation is
on** — queued jobs wait rather than fail. If that ever blocks a release, delete
the `CI_RUNNER` variable and the next run uses GitHub's runners.

## VPS runner (deploy only)

> **The VPS runner is a *user* systemd service under the `ci` user, and it dies
> with linger.** It lives at `/home/ci/.config/systemd/user/actions-runner.service`,
> not in system systemd, so `loginctl disable-linger ci` stops it and
> `contabo-ci` goes offline — which silently blocks every deploy, because
> `DEPLOY_RUNNER=vps-deploy` resolves only to that runner. This happened on
> 2026-09-16 while pruning an unrelated idle rootless Docker daemon owned by
> the same user; the daemon was genuinely abandoned, the runner was not.
>
> Restore with:
>
> ```bash
> sudo loginctl enable-linger ci
> sudo -u ci env XDG_RUNTIME_DIR=/run/user/1005 \
>   DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1005/bus \
>   systemctl --user enable --now actions-runner.service
> ```
>
> Before disabling linger for any user, check what user services it is holding
> up: `ls ~<user>/.config/systemd/user/`. The commands below describe the
> intended install; the live one differs.

As a non-root user on the VPS, with Docker group membership and the `sudo`
rights `ci-deploy.sh` already needs for `caddy validate`, `install` and
`systemctl reload caddy`:

```bash
mkdir -p ~/actions-runner && cd ~/actions-runner
curl -o runner.tar.gz -L https://github.com/actions/runner/releases/latest/download/actions-runner-linux-x64.tar.gz
tar xzf runner.tar.gz
./config.sh --url https://github.com/ShiunduZachariah/sports-prediction-platform \
            --token <REGISTRATION_TOKEN> --labels vps-deploy --name vps
sudo ./svc.sh install && sudo ./svc.sh start
```

## Switching over, and back

The workflow reads repository variables rather than hard-coding labels:

```yaml
runs-on: ${{ vars.CI_RUNNER || 'ubuntu-latest' }}        # every gate job
runs-on: ${{ vars.DEPLOY_RUNNER || 'ubuntu-latest' }}    # the deploy job
```

Settings → Secrets and variables → Actions → Variables:

- `CI_RUNNER` = `local-ci`
- `DEPLOY_RUNNER` = `vps-deploy`

**To roll back instantly, delete the variable.** The next run goes straight
back to `ubuntu-latest` with no commit.

The indirection is the point. Hard-coding `runs-on: self-hosted` before a
runner is registered makes jobs queue forever rather than fail, and the only
way out is another commit — through the pipeline that is stuck.

## What to weigh

- **Private repository only.** GitHub advises against self-hosted runners on
  public repositories, because a fork's pull request can execute arbitrary code
  on the runner. This repository is private, which is the supported
  configuration — but anyone who can open a pull request here can run code on
  these machines. Keep collaborator access tight, and note the VPS runner is on
  the production host.
- **Availability.** A queued job with no online runner waits rather than
  failing. If the workstation is off, CI does not run. That is usually fine;
  when it is not, delete `CI_RUNNER` and the next run uses GitHub's runners.
- **Disk.** The runner keeps a work directory and a tool cache. Budget a few GB
  on each machine, and prune Docker periodically on the workstation — the VPS
  already does this after every successful deploy.

## Deploying without GitHub at all

Running `ci-deploy.sh` from a laptop works and costs nothing, but it forfeits
what this setup preserves: there is no run record, no gate matrix, and the
tarball is built from a working tree that may not match `origin/main`.
Uncommitted files ship silently. Keep that strictly for emergencies.
