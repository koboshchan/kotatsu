# Kotatsu

[![CI](https://github.com/koboshchan/kotatsu/actions/workflows/ci.yml/badge.svg)](https://github.com/koboshchan/kotatsu/actions/workflows/ci.yml)

Self-hosted machine inventory and remote terminals. Linux agents connect outbound to your server, and you open interactive shells from the `kotatsu` CLI. No inbound agent ports, network tunnels or port forwarding.

> [!IMPORTANT]
> Shells run as a dedicated unprivileged service account, not your personal user. The server relays live terminal bytes and must be trusted. This is transport TLS, not end-to-end encryption or a sandbox for untrusted code.

## Features

- Account-owned machine inventory in a CLI and plain browser UI.
- Interactive Linux PTYs with resizing, exit codes and automatic agent reconnection.
- Separate account, enrollment and machine credentials with expiry and revocation.
- Short-lived installers with pinned, checksummed Linux agent downloads.
- SQLite storage, Docker Compose configuration and hardened systemd agent units.
- Standalone CLI builds for Linux amd64/arm64, macOS arm64 and Windows amd64/arm64.

## Installation and quick start

Build from source with Go 1.27.2 or later. Release packaging also requires GNU tar, gzip and sha256sum. Agents require Linux amd64 or arm64; automatic service installation requires systemd and root access to install the dedicated account.

```sh
git clone https://github.com/koboshchan/kotatsu.git
cd kotatsu
make check
mkdir -p bin
go build -o bin/kotatsu-server ./cmd/server
go build -o bin/kotatsu ./cmd/kotatsu
go build -o bin/kotatsu-agent ./cmd/agent
KOTATSU_DEV=1 KOTATSU_LISTEN=127.0.0.1:28473 \
  KOTATSU_PUBLIC_URL=http://127.0.0.1:28473 ./bin/kotatsu-server
```

In another terminal, register and create a one-use enrollment token.

```sh
./bin/kotatsu --server http://127.0.0.1:28473 --dev auth register --name alice
./bin/kotatsu enrollment create
```

Save the API token shown once. The CLI saves its configuration under `kotatsu/config.json` in your platform's user-config directory. Writes are atomic and owner-only where supported. On Windows, use a private user profile and verify its ACLs; POSIX mode bits are not Windows ACLs.

For a manual local test, run the agent as a non-root account. The enroll command reads the enrollment token from stdin followed by a newline, rather than a command-line argument.

```sh
./bin/kotatsu-agent enroll --server http://127.0.0.1:28473 --dev --state "$HOME/.config/kotatsu-agent/agent.json"
./bin/kotatsu-agent run --state "$HOME/.config/kotatsu-agent/agent.json"
```

From your CLI terminal, list your machines and connect using the returned seven-digit ID.

```sh
./bin/kotatsu ls
./bin/kotatsu connect 1234567
```

Ctrl-] detaches locally. Unix clients use SIGWINCH for resizing; Windows clients poll console size. The browser UI at the server origin manages accounts, inventory and enrollment tokens; interactive terminals use the CLI. Browser tokens stay in page memory only.

## Commands

```text
kotatsu auth register --name alice
kotatsu auth login
kotatsu auth whoami
kotatsu auth logout
kotatsu auth tokens
kotatsu auth token-create
kotatsu auth rotate
kotatsu auth revoke <token-id>
kotatsu enrollment create [--reusable] [--ttl 15m]
kotatsu enrollment ls
kotatsu enrollment revoke <token-id>
kotatsu ls [--json]
kotatsu connect <machine-id>
kotatsu revoke-machine <machine-id>
```

Global flags are `--server`, `--config` and `--dev`. Run `kotatsu --help` or a subcommand's `--help` for details. Login reads a hidden terminal prompt or stdin. Keep credentials out of shell arguments, shared files and logs.

## Linux service installation

Publish a pinned release before using the generated installer. Set `KOTATSU_RELEASE_VERSION=v0.1.0` and `KOTATSU_RELEASE_URL=https://releases.example.com`. That origin must serve `/releases/v0.1.0/SHA256SUMS` and `kotatsu-agent_v0.1.0_linux_amd64.tar.gz` or its arm64 equivalent.

For GitHub-hosted release assets, map this path to the tagged GitHub download at your reverse proxy. Alternatively, `KOTATSU_RELEASE_DIR` serves an allowlisted local release directory through the server.

Download the private URL printed by `enrollment create` to a mode-600 temporary file, review the script, then run it with sudo on a Linux systemd host you administer. The URL and script contain a live bearer credential. Keep them out of history, logs and process arguments; delete the script after use.

The installer requires a running systemd manager and rejects non-systemd hosts before downloading or consuming an enrollment token. Use the manual agent commands above on other Linux hosts.

The installer verifies Linux prerequisites and the artifact checksum, creates the `kotatsu` service account, enrolls via stdin and enables `kotatsu-agent.service`. Identity persists at `/var/lib/kotatsu/agent.json`. Reinstalling refuses to overwrite an existing identity.

```sh
journalctl -u kotatsu-agent
sudo scripts/uninstall-agent.sh
# Also delete local credentials and the dedicated account:
sudo scripts/uninstall-agent.sh --purge
```

Checksums depend on trusting the HTTPS release origin. An attacker controlling that origin can replace both the artifact and manifest; checksums are not independent signatures.

## Docker deployment and configuration

Create a private deployment environment file and run `docker compose up -d --build`.

```dotenv
KOTATSU_PUBLIC_URL=https://shell.example.com
KOTATSU_TRUSTED_PROXIES=172.20.0.1/32
KOTATSU_RELEASE_VERSION=v0.1.0
KOTATSU_RELEASE_URL=https://releases.example.com
KOTATSU_PORT=28473
```

The proxy CIDR is an example, not a default to copy. Set your actual TLS proxy address. Compose binds the server to loopback and stores SQLite in the persistent `data` volume.

| Variable | Purpose |
| --- | --- |
| `KOTATSU_PUBLIC_URL` | Canonical HTTPS origin; defaults to `https://localhost`. |
| `KOTATSU_LISTEN` | Server bind address; defaults to `127.0.0.1:8080` outside Compose. |
| `KOTATSU_DB` | SQLite path; defaults to `data/kotatsu.db`. |
| `KOTATSU_TRUSTED_PROXIES` | Comma-separated trusted forwarding CIDRs; empty by default. |
| `KOTATSU_RELEASE_VERSION` | Pinned agent release tag. |
| `KOTATSU_RELEASE_URL` | HTTPS origin serving pinned release artifacts. |
| `KOTATSU_RELEASE_DIR` | Optional local directory for allowlisted release files. |
| `KOTATSU_PORT` | Compose loopback host port; defaults to `28473`. |
| `KOTATSU_DEV` | Set to `1` for explicit development-only HTTP. |

Your reverse proxy must terminate TLS, forward WebSocket upgrades and overwrite forwarding headers rather than append untrusted client values. Disable caching for API and installer paths. Never log `/script/*` paths, authorization headers, request bodies or terminal data.

HTTPS is required outside development, either directly or asserted by an explicitly trusted proxy. The unauthenticated `/healthz` liveness endpoint is the only cleartext exception and exposes no account data. Registration is open but rate-limited; add access controls at your private TLS edge if public registration is unwanted.

## Architecture and security

One Go module contains `cmd/server`, `cmd/kotatsu` and `cmd/agent`. The server embeds the HTML/JavaScript UI and uses SQLite with WAL and foreign keys. Versioned REST endpoints under `/api/v1` handle accounts, credentials, enrollment, inventory and revocation; authenticated WebSockets carry agent control and terminal sessions.

Account API tokens, enrollment tokens and machine credentials are distinct random bearer tokens. Only hashes are stored. Ownership checks protect machine and session operations; seven-digit display IDs are convenience identifiers, not secrets. Enrollment defaults to one use and 15 minutes, with explicit reusable tokens capped at 24 hours.

Agents reconnect with bounded exponential backoff and jitter. Heartbeats run every 30 seconds; machines appear offline after 90 seconds. Sessions allow four concurrent shells per machine and expire after one hour or ten idle minutes. Revocation closes affected connections. Audit records retain IDs, timestamps and end reasons, not terminal transcripts.

The hardened agent unit restricts writes to its state/home directory and temporary files, with no sudo or access to protected home directories. A shared service user is not isolation for hostile multi-tenant workloads. Install agents only on machines you administer.

Run a single server instance. A restart reconnects agents but does not resume interactive sessions. For backups, stop the server before copying SQLite files or use SQLite's online backup facilities. Keep backups private.

## Build and validation

```sh
make check
VERSION=v0.1.0 ./scripts/release.sh
(cd dist && sha256sum -c SHA256SUMS)
```

`make check` runs race tests, executable builds and vet. The release script cross-compiles five CGO-free CLI targets and two Linux agent targets, embeds version metadata and writes checksums with deterministic archive headers. Push/PR CI runs these checks and a Docker build; tagged CI publishes release archives.

> [!NOTE]
> Live Linux tests passed through a public HTTPS/WebSocket tunnel with three independent agent sandboxes and two accounts: pinned installer downloads and checksums, enrollment, account isolation, PTYs, resize/exit codes, credential revocation, agent/server reconnect, 90-second offline detection and CLI terminal restoration. Browser checks verified isolated inventories, logout and no persisted credentials after reload. These sandboxes use direct agent execution because PID 1 is not systemd. Real Docker deployment and real systemd service runtime remain untested. Cross-compilation is not runtime validation on macOS, Windows or Linux arm64.

See [PROGRESS.md](PROGRESS.md) for implementation and validation details, and [PLAN.md](PLAN.md) for the original scope.
