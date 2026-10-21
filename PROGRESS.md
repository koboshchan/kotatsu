# AnyShell build progress

The complete PLAN.md was read before implementation. No ancestor/repository CLAUDE.md or AGENTS.md exists. All development and testing stays on the anyshell sandbox. Nothing will be pushed.

## Step 1
Established the Go module, three executable boundaries, shared API types, version metadata, private config-file writes, URL policy, and build/test commands. Selected modernc SQLite, coder/websocket, creack/pty, x/term, Cobra and Google UUID. Chub has no matching Go guides; checked the installed package reference with go doc instead. Plain HTML/JS without CSS is the explicit UI requirement.

## Step 2
Added transactional SQLite migration/schema with foreign keys, UUIDs, unique token hashes, and account token registration, exchange (/me), issue, atomic rotation, listing and owner-scoped revocation. Authentication never persists raw credentials. Added bounded registration rate limiting, JSON size/schema limits, no-store responses, cross-origin mutation rejection, trusted-proxy-only forwarding, structured logs without request URLs, and a gracefully terminating HTTP entry point. Token lifecycle, persistence, permissions, and API authorization tests pass.

## Step 3
Implemented atomic one-time/reusable expiring enrollment, separate machine credentials, collision-checked seven-digit IDs, owner-only inventory, observed-IP heartbeats and 90-second offline calculation. Added the no-store, rate-limited /script endpoint with pinned Linux architecture artifacts, checksum verification, dedicated account and systemd service. Agent enrollment refuses existing state, stores private identity and rejects root execution. Tests cover concurrent single redemption, ID collision retry, account/machine/enrollment credential separation, expiry, revocation, heartbeat status and untrusted forwarded IP. Race tests/build/vet pass.

## Step 4
Added CLI registration/login/logout/whoami, token list/create/revoke/rotate, ls in text or JSON, and enrollment create/list/revoke. Login accepts hidden terminal input or stdin, not secret command-line flags. Config writes remain atomic/private. Added embedded semantic plain HTML and external JavaScript without CSS, owner-scoped inventory fields, registration/login and install-token controls. Web secrets exist only in page memory, no localStorage/cookies; untrusted values use textContent. CLI API integration tests and all race/build/vet checks pass.
