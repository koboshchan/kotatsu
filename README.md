# AnyShell

Self-hosted machine inventory and interactive remote shells in Go. Linux agents connect outbound to one server; no inbound agent port, network tunneling or port forwarding is implemented. Shells run as the restricted service account, not as your personal user.

## Try it locally

Requires Go 1.27.2 or later. Building release archives also needs GNU tar, gzip and sha256sum.

```sh
make check
mkdir -p bin
go build -o bin/anyshell-server ./cmd/server
go build -o bin/anyshell ./cmd/anyshell
go build -o bin/anyshell-agent ./cmd/agent
ANYSHELL_DEV=1 ANYSHELL_LISTEN=127.0.0.1:28473 \
  ANYSHELL_PUBLIC_URL=http://127.0.0.1:28473 ./bin/anyshell-server
```

In another terminal, register and create a short-lived install credential.

```sh
./bin/anyshell --server http://127.0.0.1:28473 --dev auth register --name alice
./bin/anyshell enrollment create
```

Save the API token shown once. The CLI also writes it to the platform user-config directory under `anyshell/config.json`, atomically with owner-only permissions where supported. `auth login` accepts a hidden prompt or stdin. Do not put credentials in shell arguments, shared files or public logs. On Windows, use your private user profile and verify its ACLs; POSIX mode bits do not establish a Windows ACL.

For manual sandbox enrollment, run the agent as a non-root account. Pass the install token on stdin and choose a private state path.

```sh
./bin/anyshell-agent enroll --server http://127.0.0.1:28473 --dev --state "$HOME/.config/anyshell-agent/agent.json"
./bin/anyshell-agent run --state "$HOME/.config/anyshell-agent/agent.json"
./bin/anyshell ls
./bin/anyshell connect 1234567
```

Replace 1234567 with your machine ID. Ctrl-] detaches locally. Terminal dimensions and TERM are negotiated; Unix clients handle SIGWINCH, and Windows clients poll console size. The browser UI is available at the server origin, using the same owner-scoped API. Its token stays in page memory only.

## Linux service installation

Publish a pinned release first. Configure `ANYSHELL_RELEASE_VERSION=v0.1.0` and `ANYSHELL_RELEASE_URL=https://releases.example.com`. The origin must serve `/releases/v0.1.0/SHA256SUMS` and the matching `anyshell-agent_v0.1.0_linux_amd64.tar.gz` or arm64 archive. For GitHub releases, use a reverse proxy mapping this path to the tagged GitHub download; the server also supports `ANYSHELL_RELEASE_DIR` for a directory containing allowlisted release files.

Open the private installer URL returned by `enrollment create`, download the script to a mode-600 temporary file, review it, then run it with sudo on a Linux systemd host you administer. Avoid a credential-bearing URL in command history or process arguments. The script contains a live enrollment token, so delete it after use.

The installer checks Linux architecture and prerequisites, verifies the exact artifact against the pinned SHA-256 manifest, creates an `anyshell` system account, enrolls without putting the token in process arguments, and enables `anyshell-agent.service`. A machine restart retains its credential in `/var/lib/anyshell/agent.json`.

Checksums authenticate the artifact only to the trusted HTTPS release origin. Compromise of that origin can replace both artifact and manifest; do not treat a checksum as an independent signature.

Inspect service logs with `journalctl -u anyshell-agent`. Reconnect uses bounded exponential backoff with jitter. Heartbeats run every 30 seconds; inventory goes offline after 90 seconds without a heartbeat. Re-running the installer refuses to overwrite an existing identity.

To uninstall, run `sudo scripts/uninstall-agent.sh`. Add `--purge` only when you intend to delete that machine's local credential and the dedicated account.

## Deploy with Docker Compose

Set these values in a private deployment environment file, then run `docker compose up -d --build`.

```dotenv
ANYSHELL_PUBLIC_URL=https://shell.example.com
ANYSHELL_TRUSTED_PROXIES=172.20.0.1/32
ANYSHELL_RELEASE_VERSION=v0.1.0
ANYSHELL_RELEASE_URL=https://releases.example.com
ANYSHELL_PORT=28473
```

The proxy CIDR above is an example, not a safe default. Set the exact observed address of your own TLS proxy. Bind the application privately; the example Compose publishes only on loopback. The reverse proxy must terminate TLS, forward WebSocket upgrades and overwrite `X-Forwarded-Proto` and `X-Forwarded-For` rather than append untrusted client values. Never log `/script/*` paths, authorization headers, API bodies or terminal data. Disable proxy caching for all API and script paths.

The server requires HTTPS outside `ANYSHELL_DEV=1`, either directly observed TLS or HTTPS asserted by an explicitly trusted proxy. The unauthenticated `/healthz` liveness check is the only cleartext exception. It returns no account data.

SQLite lives in the persistent `data` volume with WAL and foreign keys. The server is one instance; do not run multiple replicas against one database or assume relays survive a server restart. Stop the server before taking a filesystem backup, or use SQLite's online backup facilities. Keep backups private. On restart, unfinished audit records are marked ended; agents reconnect, but interactive sessions do not resume.

Additional settings are `ANYSHELL_LISTEN`, `ANYSHELL_DB` and `ANYSHELL_RELEASE_DIR`. The service account can write only its home/state directory and temporary files under the hardened unit. It cannot access your protected home or sudo.

Registration is open but rate-limited. Put access controls at your private TLS edge if the server must not accept registrations from the public Internet.

## Commands and API

Run `anyshell --help`, `anyshell auth --help` and `anyshell enrollment --help` for the complete command set. Account tokens support create, list, revoke and atomic rotate. Install tokens default to one use and 15 minutes, with explicit reusable tokens capped at 24 hours.

The API prefix is `/api/v1`. Routes cover `accounts`, `me`, `tokens`, `enrollment-tokens`, `machines`, `machines/enroll`, `agent/heartbeat`, `agent/connect` and `machines/{seven-digit-id}/connect`. Tokens are typed random bearer credentials; only hashes are stored. Internal records use UUIDs; seven-digit machine IDs are convenience identifiers, not secrets. All inventory and session operations check account ownership.

Sessions permit four concurrent shells per machine, expire after one hour or ten idle minutes, recheck the initiating token during use, and store only session/account/machine IDs, timestamps and an end reason. No terminal transcript is retained. The server is a trusted relay and can see live terminal bytes; this is transport TLS, not end-to-end encryption. Do not execute untrusted commands or deploy agents on machines you do not administer.

## Build and validation

```sh
make check
VERSION=v0.1.0 ./scripts/release.sh
(cd dist && sha256sum -c SHA256SUMS)
```

The release script builds standalone CGO-free CLI archives for Linux amd64/arm64, macOS arm64, and Windows amd64/arm64, plus Linux amd64/arm64 agents. Artifacts include version metadata and deterministic archive headers. Tagged CI runs tests and publishes the archives and SHA256SUMS; ordinary push/PR CI runs race tests, vet, cross-builds and a Docker build. No GitHub repository or remote is created by this source tree.

See [PROGRESS.md](PROGRESS.md) for the validation receipt and known environment limits, and [PLAN.md](PLAN.md) for the original scope.
