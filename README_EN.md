# SoftEther Manager

**English** | [简体中文](README.md)

A self-hosted web panel that manages the [SoftEther VPN Server](https://www.softether.org/)
across multiple hosts through its JSON-RPC management API, alongside live monitoring
of the machine running the panel.

One command installs the panel beside your SoftEther instance. Everything after that —
users, policies, sessions, access control, SecureNAT, certificates, cascades, the works —
happens in the browser.

![Dashboard](docs/screenshots/en/dashboard.png)

These previews are captured from the current Go backend and frontend running with
isolated demo data. This page shows the English interface; the Chinese README uses
the Chinese interface.

## What it does

**Watches the machine.** The dashboard opens with the server's own health, read straight
from the kernel: CPU (with per-core detail, load, and steal time — the figure that
explains a slow VPS nothing else accounts for), memory and swap, every real filesystem,
and live network throughput, each with a few minutes of sparkline history.

**Switches between servers.** Save, edit, test and remove connections from the server
list. Search and switch from the top bar; each browser tab selects its own target.
Traffic, quotas, sessions, VPN credentials and audit records are isolated by server.
Panel accounts, domain and HTTPS settings are shared. Resource telemetry always
refers to the panel host.

**Manages SoftEther.** The panel is built on a complete client for the
SoftEther JSON-RPC suite, organised by where things
live:

| | |
| --- | --- |
| **Users** | create, edit, delete; password / certificate / signed / RADIUS / NT auth; group membership; expiry; per-user security policy (all forty knobs, grouped and explained); lifetime transfer counters; usage charts; live sessions with a kill switch; a one-click **.vpn connection file** download (optionally with the credential embedded, hashed the way the client stores it) — plus one **Users** page listing everyone across every hub, sortable by any column and searchable by name, hub, group or note |
| **Traffic limits** | a byte ceiling per config *and* per hub, counting download only, upload only, or both, in MB / GB / TB, measured against the very Transfer figure the tables show — reached, the panel denies the config's access and cuts its sessions (or takes the hub offline), and restores exactly what it found when the limit is raised or the transfer reset. SoftEther cannot zero a counter, so the panel keeps its own zero and every figure it prints subtracts it: **Reset transfer** really does put the column back to 0 B |
| **Groups** | create, edit, delete; group-wide security policy that members inherit |
| **Sessions** | live table with client address, protocol, cipher and transfer; per-session detail with the session's own traffic curve; disconnect; and a session log that outlives the connection, so "who was on last night, from where, moving what" is still answerable in the morning |
| **Access control** | the packet filter (priority, protocol, addresses, ports, users, delay/jitter/loss simulation) and the source-IP limit list |
| **Hub security** | trusted CA certificates, certificate revocation entries, RADIUS delegation, hub password |
| **SecureNAT** | enable/disable, virtual NAT and DHCP configuration, live NAT and lease tables |
| **Cascades** | hub-to-hub links to other servers, with live link status |
| **Switching** | MAC and IP address tables, local bridges, virtual layer-3 switches with interfaces and routes |
| **Server** | listeners, VPN-over-ICMP/DNS, L2TP/IPsec, OpenVPN and SSTP clones, VPN Azure, dynamic DNS, cipher, certificate (view / replace / regenerate), clustering, keep-alive, syslog, EtherIP IDs, a paged browser for the server and security logs, config backup and restore |
| **The panel itself** | a custom domain with a Let's Encrypt certificate the panel obtains and renews on its own, HTTPS on a port of your choosing, and a switch that makes the domain the only way in — a request by IP address gets a bare 404 |

**Draws what the numbers mean.** SoftEther keeps cumulative counters and no history, so
the panel samples them on a schedule and turns the deltas into throughput charts — per
hub, per user, and per session. Each user gets an identity card: who they are, how they
authenticate, what they've moved, whether they're online right now. Every sampler has
its own clock and its own switch in Settings, so a small box can keep the cheap ones and
turn the expensive ones off.

![User detail](docs/screenshots/en/user.png)

**Stops what has had enough.** Any config, and any hub, can carry a traffic ceiling: an
amount in MB, GB or TB, counting the download, the upload, or the two together. It is
measured against *the number already on screen* — the Transfer column — so a 300 GB
limit set on a config that has moved 144 GB starts at 144 GB, not at nothing. At the
ceiling the config is denied access and its sessions are cut, or the hub is taken
offline; the moment the limit is raised or the transfer reset, exactly the policy the
panel found is put back.

SoftEther counts forever and has no API to zero a counter, so the panel keeps its own
zero and every figure it prints subtracts it. **Reset transfer** moves that zero to
today, which puts the Transfer column and the limit back to nothing together — they
were never two numbers. A limit is edited where it belongs, on the config's profile,
and the user tables carry the meter so a full one is visible without opening anything.

**Says what state everything is in.** Every switch in the panel — a hub's online state, a
listener, SecureNAT, VPN Azure, a cascade, a layer-3 switch, a traffic limit — is a real
switch: it locks while the request is in flight, then shows the state *read back from the
server*, not the state that was clicked. A change that did not take says so, with the
reason, next to the control and in a toast.

More rooms of the house — every user across every hub, who is connected right now, one
Virtual Hub's home, and the whole thing in its light theme:

![Users](docs/screenshots/en/users.png)

<details>
<summary><b>Virtual Hubs</b> — network segments and their users and sessions</summary>

![Virtual Hubs](docs/screenshots/en/hubs.png)
</details>

<details>
<summary><b>Logs</b> — server files and paginated log content</summary>

![Logs](docs/screenshots/en/logs.png)
</details>

<details>
<summary><b>Sessions</b> — who is connected, from where, over what, moving how much</summary>

![Sessions](docs/screenshots/en/hub-sessions.png)
</details>

<details>
<summary><b>Hub overview</b> — status, throughput, and the tabs where everything lives</summary>

![Hub](docs/screenshots/en/hub-overview.png)
</details>

<details>
<summary><b>Server settings</b> — listeners, protocols, encryption, bridges, L3 switches, clustering…</summary>

![Server settings](docs/screenshots/en/server-settings.png)
</details>

<details>
<summary><b>Light theme</b> — a first-class mirror, not an inversion</summary>

![Light theme](docs/screenshots/en/hub-overview-light.png)
</details>

## The mobile panel

Below 960px the interface becomes a different application, not a squeezed one: a top bar
that knows where you are, a bottom tab bar, cards instead of tables, and bottom sheets
for decisions. Managing a VPN server from a phone is a first-class path.

<p align="center">
  <img src="docs/screenshots/en/mobile-dashboard.png" width="24%" alt="Mobile dashboard" />
  <img src="docs/screenshots/en/mobile-users.png" width="24%" alt="Mobile users" />
  <img src="docs/screenshots/en/mobile-user.png" width="24%" alt="Mobile user detail" />
  <img src="docs/screenshots/en/mobile-hub.png" width="24%" alt="Mobile hub" />
</p>

## Install

On the Linux server that runs (or will run) SoftEther, as root:

```bash
bash <(curl -Ls https://raw.githubusercontent.com/siryle/Softether-Manager/master/scripts/install.sh)
```

The installer asks four questions — username, password, port, and a secret web path the
panel hides behind — and answers everything else itself: it downloads the native Go
executable for the host architecture, verifies its checksum, writes a
hardened systemd unit, starts the service and creates your account. There is no database
to set up: the panel keeps everything in one SQLite file under
`/var/lib/softether-manager`, and backing it up is copying that directory.
Running the panel requires neither Python nor a Go compiler.

First sign-in, the panel asks for the SoftEther management port and administrator
password (127.0.0.1:5555 on a stock install), tests them, and connects. From then on it
is the server's control room.

Non-interactive, for automation:

```bash
read -r -s -p 'Panel password: ' sem_password
printf '\n'
printf '%s' "$sem_password" | bash <(curl -Ls https://raw.githubusercontent.com/siryle/Softether-Manager/master/scripts/install.sh) \
  --non-interactive --username admin --password-stdin --port 8443 --path mysecret --json
unset sem_password
```

Running the installer again on a host that already has the panel opens `sem`, the
management CLI, instead of reinstalling:

```
sem status               version, service state and the URL the panel is on
sem update               install the newest release
sem password             reset an account password when nobody can sign in
sem users                list the panel accounts
sem port / sem path      move the panel
sem domain               the domain, its certificate, and domain-only access
sem logs -f              follow the service log
sem uninstall            remove the panel (the database survives unless you say otherwise)
```

### Updating

The panel updates itself: the version pill in the corner checks the releases feed, and
one click installs the new version through the same installer an operator would use —
launched in a transient systemd unit so it survives the restart it performs. A release
that lands while the panel is open announces itself once, with the updater on the
notice. `sem update` does the identical thing from a shell.

### A domain and HTTPS

Point a DNS record at the server, open port 80 and the HTTPS port, and enter the name
under **Settings → Domain & HTTPS**. The panel registers with Let's Encrypt, answers the
validation on port 80, obtains the certificate, starts serving HTTPS on the port you chose
(443 unless SoftEther's own listener is sitting on it — then disable that listener, or
pick another port), keeps port 80 answering with a redirect, and renews the certificate
a month before it expires. Each stage — the domain, its DNS, port 80, the certificate,
the renewal, the HTTPS listener — reports its own state and its own error on the page,
so a record that points elsewhere, a firewall that blocks port 80 and a port SoftEther
already holds read as three different problems, not one "failed". The account key, the
certificate and its private key live in the data directory with everything else the
panel owns.

Once you are using the panel through the domain, **Only serve the panel at this domain**
turns away every request that arrives by IP address or under any other name, on every
port, with the same anonymous 404 the panel's root already gives. The switch is only
offered from the domain itself, so it cannot lock you out; requests from the machine
itself and the certificate validation path are always let through, so renewal and the
installer keep working. If a DNS change ever does strand you, `sem domain only off` on
the server lifts it.

## Scope: one server, on purpose

This panel deliberately manages **only the SoftEther instance on its own machine**. It
holds one connection, one database, one audit trail — which is what makes it simple to
install, simple to reason about, and safe to hand to the machine's operator.

Fleet-level concerns — installing SoftEther on fresh hosts, managing many of these
panels, creating users across instances — belong to a separate master panel that speaks
to many single-server panels like this one. That separation keeps each layer honest:
this panel is the complete, self-sufficient control room for one server; the master
panel (a separate project) orchestrates many of them.

## Architecture

```
/proc ────────┐
              ├──  Go net/http backend  →  Next.js panel
SoftEther ────┘    (internal/manager)       (app/web)
 JSON-RPC
```

One monolithic package, one process, one SQLite file:

- **`cmd/softether-manager/`** — the service and management CLI entry point.
- **`internal/manager/`** — the Go backend, using standard-library `net/http`.
  It shapes SoftEther JSON-RPC into the existing `/api/v1/` REST API, stores panel
  state using pure Go SQLite, samples resources, traffic and sessions, enforces
  quotas, manages certificates and updates, and serves the built frontend.
  RPC definitions are embedded in the executable.
- **`app/web/`** — the Next.js frontend, exported as a single hash-routed page so the
  same build serves from any secret path prefix. Dark and light themes, desktop and
  mobile shells.

Secrets stay where they belong: the SoftEther administrator password is
Fernet-encrypted in the database, as are reusable VPN user credentials; the session-signing and encryption keys are generated
on first start into the data directory, and so are the Let's Encrypt account key and the
panel's certificate; the environment file under `/etc` is readable only by root.
Browsers authenticate through HttpOnly cookies. Password changes invalidate old
sessions; signing out revokes that login across restarts. Eight failed login attempts
from the same source block further attempts for five minutes. Upgrading requires
signing in again, and existing VPN credentials are encrypted automatically. Keep the
database and `encryption.key` together; old backups may still contain plain credentials.

SoftEther management connections pin the certificate's SHA-256 fingerprint on first
use. A mismatch blocks administrator credentials before any HTTP request is sent.
After a legitimate replacement, use Servers → Edit → Inspect saved server certificate,
compare the fingerprint independently with the server, then confirm trust. Establish
the first connection over a trusted network.

The Linux installer creates the account before opening the panel listener and passes
passwords through standard input. Its optional SoftEther installation uses pinned
`v4.44-9807-rtm` archives for amd64 and arm64, checked against recorded SHA-256 hashes
before unpacking or execution.

The certificate comes from the Go ACME client (`internal/manager/tls.go`, HTTP-01),
so there is no certbot installation or cron job, and the service's
`ProtectSystem=full` sandbox stays as tight as it was: everything the certificate needs
is in the data directory.

## Development

Use the Go version specified in `go.mod`, plus Node.js 22 and npm.

After cloning, run `git config --local core.hooksPath .githooks` once to enable
automatic version bumps on commit. Each commit with staged changes increments
the patch number in `VERSION` and includes it in the same commit, for example
`1.0.13 → 1.0.14`. Explicitly staged version changes are preserved, and retrying
a failed commit does not increment it again.

```bash
go run ./cmd/softether-manager        # backend on :8000, serves app/web/out if built

# In another terminal:
cd app/web
npm ci
NEXT_PUBLIC_API_BASE=http://localhost:8000/ npm run dev   # point it at the backend
# Open http://localhost:3000 too: use the same hostname so cookies are shared.

npm test                             # server switching and configuration save behavior
npm run build                        # export frontend to app/web/out
cd ../..
go build -o softether-manager ./cmd/softether-manager
go test -race ./...
go vet ./...
python3 scripts/test-install.py
./softether-manager serve             # serve the backend and built frontend
```

The default data directory is `./data`; override it with `SEM_DATA_DIR`. Existing
`SEM_*` configuration and frontend API paths continue to work. Resource monitoring
reads Linux `/proc`; macOS development hosts report it as unavailable.

Backend tests use a local TLS stand-in for SoftEther, verify the legacy Python crypto
and VPN-file formats, and check persistence, quotas and the REST route contract.

Access-list replacements and deletions require the list's returned `version` in the
request JSON as `expected_version`. Stale versions return 409; missing versions return
428. Read the current list before writing it to avoid overwriting another tab's changes.

Releases are tag-driven: CI builds the frontend, packages one tarball, and publishes it
with a checksum on every `v*` tag. The archive contains Linux amd64 and arm64
executables. Build it locally with `bash scripts/build-release.sh --version vX.Y.Z`.
The installer and the in-panel updater both follow the installed repository.

Without an explicit version, local packaging and CI use the exact tag on HEAD,
or fall back to `VERSION`. Docker and the panel also read this file. Publishing
a GitHub release still requires pushing a `vX.Y.Z` tag.

To upgrade from Python, stop the old service and back up the entire data directory,
then run the new installer or replace the container. Keep `manager.db`, `session.key`,
`encryption.key` and `tls/` together: existing accounts, encrypted connection details,
current-schema history, quotas and certificates retain their formats. Run only one
version against the database at a time. Single-server databases migrate transactionally while retaining accounts, credentials, quotas and history. Only early sample tables without hub names are rebuilt.

## Security notes

- Out of the box the panel serves plain HTTP. Give it a domain (Settings → Domain &
  HTTPS) and it serves HTTPS with a Let's Encrypt certificate it renews itself; or put a
  TLS-terminating reverse proxy in front of it, or bind it to localhost and reach it over
  an SSH tunnel or WireGuard. A session issued over HTTPS carries a `Secure` cookie and is
  never sent back over the plain port.
- With a domain set, *Only serve the panel at this domain* refuses everything that does
  not name it — the bare IP address included — with an anonymous 404.
- The secret web path keeps scanners out of the login form; it is defence in depth, not
  authentication. The authentication is the account password (scrypt-hashed) and
  HttpOnly session cookies.
- Set `SEM_ADMIN_USERNAME` and `SEM_ADMIN_PASSWORD` before starting a public container.
  If left empty, whoever first completes setup can create the administrator account.
- The panel talks to SoftEther on the loopback interface by default, so the management
  password never crosses a network.
