package server

import (
	"fmt"
	"github.com/koboshchan/kotatsu/internal/api"
	"github.com/koboshchan/kotatsu/internal/auth"
	"github.com/koboshchan/kotatsu/internal/config"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$`)

func (s *Server) enrollmentRoutes() {
	s.handle("DELETE "+api.Prefix+"/machines/{id}", s.revokeMachine)
	s.handle("GET "+api.Prefix+"/machines", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.Machines(r.Context(), identity(r).Account.ID)
		result(w, v, e)
	})
	s.handle("GET "+api.Prefix+"/enrollment-tokens", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.Enrollments(r.Context(), identity(r).Account.ID)
		result(w, v, e)
	})
	s.handle("POST "+api.Prefix+"/enrollment-tokens", s.createEnrollment)
	s.handle("DELETE "+api.Prefix+"/enrollment-tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		e := s.Store.RevokeEnrollment(r.Context(), identity(r).Account.ID, r.PathValue("id"))
		result(w, map[string]bool{"revoked": true}, e)
	})
	s.mux.HandleFunc("POST "+api.Prefix+"/machines/enroll", s.enroll)
	s.mux.HandleFunc("POST "+api.Prefix+"/agent/heartbeat", s.heartbeat)
	s.mux.HandleFunc("GET /script/{token}", s.script)
}
func (s *Server) createEnrollment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reusable bool `json:"reusable"`
		TTL      int  `json:"ttl_seconds"`
		Short    bool `json:"short"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.TTL == 0 {
		in.TTL = 900
	}
	if in.TTL < 1 || in.TTL > 86400 {
		fail(w, 400, "TTL must be 1-86400 seconds")
		return
	}
	e, err := s.Store.CreateEnrollment(r.Context(), identity(r).Account.ID, in.Reusable, time.Duration(in.TTL)*time.Second, in.Short)
	if err != nil {
		fail(w, 500, "could not create enrollment token")
		return
	}
	respond(w, 201, e)
}
func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	if !s.allow(r, "enroll") {
		fail(w, 429, "too many requests")
		return
	}
	var in api.EnrollRequest
	if !decode(w, r, &in) {
		return
	}
	if auth.Bearer("Bearer "+in.Token, "ae") == "" && !api.IsShortCode(in.Token) {
		fail(w, 401, "enrollment token required")
		return
	}
	if len(in.Name) < 1 || len(in.Name) > 255 || len(in.Platform) > 80 {
		fail(w, 400, "invalid metadata")
		return
	}
	out, err := s.Store.Enroll(r.Context(), in, s.publicIP(r))
	if err != nil {
		fail(w, 401, "invalid, expired or consumed enrollment token")
		return
	}
	respond(w, 201, out)
}
func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	raw := auth.Bearer(r.Header.Get("Authorization"), "am")
	if raw == "" {
		fail(w, 401, "machine credential required")
		return
	}
	m, err := s.Store.MachineAuth(r.Context(), raw)
	if err != nil {
		fail(w, 401, "invalid machine credential")
		return
	}
	err = s.Store.Heartbeat(r.Context(), m.ID, s.publicIP(r))
	result(w, map[string]bool{"ok": true}, err)
}
func shellQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
func (s *Server) script(w http.ResponseWriter, r *http.Request) {
	if !s.allow(r, "script") {
		fail(w, 429, "too many requests")
		return
	}
	token := r.PathValue("token")
	isShort := api.IsShortCode(token)
	valid := false
	if isShort {
		valid = s.Store.EnrollmentValidByShortCode(r.Context(), token) == nil
	} else {
		valid = auth.Bearer("Bearer "+token, "ae") != "" && s.Store.EnrollmentValid(r.Context(), token) == nil
	}
	if !valid {
		fail(w, 401, "invalid enrollment token")
		return
	}
	if !releaseVersion.MatchString(s.Config.ReleaseVersion) || s.Config.ReleaseURL == "" {
		fail(w, 503, "a pinned agent release has not been configured")
		return
	}
	if err := config.ValidateURL(s.Config.ReleaseURL, s.Config.Dev); err != nil {
		fail(w, 503, "invalid release origin")
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=kotatsu-install.sh")
	dev := ""
	if s.Config.Dev {
		dev = "--dev"
	}
	_, _ = fmt.Fprintf(w, installer, shellQuote(s.Config.PublicURL), shellQuote(token), shellQuote(s.Config.ReleaseURL), shellQuote(s.Config.ReleaseVersion), shellQuote(dev), strconv.FormatBool(s.Config.Dev))
}

// All format placeholders are confined to quoted variable assignments.
// A separate script test exercises unsupported architectures and checksum failures.
const installer = `#!/bin/sh
set -eu
SERVER=%s
TOKEN=%s
RELEASE_ORIGIN=%s
VERSION=%s
DEV_FLAG=%s
DEVELOPMENT=%s
die() { echo "Kotatsu: $*" >&2; exit 1; }
echo "Kotatsu: checking Linux/systemd prerequisites"
[ "$(uname -s)" = Linux ] || die "only Linux is supported"
case "$(uname -m)" in x86_64|amd64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) die "unsupported architecture" ;; esac
AS_ROOT=0
if [ "$(id -u)" = 0 ]; then AS_ROOT=1; fi
SYSTEMD_AVAILABLE=0
if command -v systemctl >/dev/null 2>&1 && systemctl show-environment >/dev/null 2>&1; then
  SYSTEMD_AVAILABLE=1
fi
command -v curl >/dev/null || die "install curl first"
command -v sha256sum >/dev/null || die "install sha256sum first"
command -v tar >/dev/null || die "install tar first"
if [ "$AS_ROOT" = 1 ]; then
  for tool in getent useradd install runuser awk; do command -v "$tool" >/dev/null || die "install missing prerequisite $tool first"; done
else
  for tool in getent install awk; do command -v "$tool" >/dev/null || die "install missing prerequisite $tool first"; done
fi
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT HUP INT TERM
PROTO='=https'
[ "$DEVELOPMENT" != true ] || PROTO='=http,https'
fetch() { curl -L --fail --silent --show-error --proto "$PROTO" --proto-redir "$PROTO" --tlsv1.2 "$1" -o "$2"; }
ARTIFACT="kotatsu-agent_${VERSION}_linux_${ARCH}.tar.gz"
echo "Kotatsu: downloading pinned release $VERSION for $ARCH"
fetch "$RELEASE_ORIGIN/$VERSION/$ARTIFACT" "$WORK/$ARTIFACT" || die "agent download failed"
fetch "$RELEASE_ORIGIN/$VERSION/SHA256SUMS" "$WORK/SHA256SUMS" || die "checksum download failed"
(cd "$WORK"; awk -v artifact="$ARTIFACT" '$2 == artifact { print }' SHA256SUMS > selected; [ "$(wc -l < selected)" -eq 1 ] && sha256sum -c selected) || die "checksum verification failed; nothing installed"
tar -tzf "$WORK/$ARTIFACT" > "$WORK/contents"
[ "$(cat "$WORK/contents")" = kotatsu-agent ] || die "unexpected release archive contents"
tar -xzf "$WORK/$ARTIFACT" -C "$WORK" --no-same-owner
[ -f "$WORK/kotatsu-agent" ] && [ ! -L "$WORK/kotatsu-agent" ] || die "invalid agent artifact"
if [ "$AS_ROOT" = 1 ]; then
  echo "Kotatsu: creating restricted service account"
  getent passwd kotatsu >/dev/null || useradd --system --home-dir /var/lib/kotatsu --create-home --shell /usr/sbin/nologin kotatsu
  install -d -m 0700 -o kotatsu -g kotatsu /var/lib/kotatsu
  INSTALL_DIR=/usr/local/bin
  INSTALL_USER=kotatsu
  STATE_DIR=/var/lib/kotatsu
  SERVICE_FILE=/etc/systemd/system/kotatsu-agent.service
  PROTECT_HOME=true
  WANTED_BY=multi-user.target
else
  INSTALL_USER=$(id -un)
  # Web/PHP contexts often have no HOME, or a HOME owned by another user.
  # Pick the first writable candidate, then fall back to a private temp dir.
  BASE=
  for candidate in "${HOME:-}" "$(getent passwd "$(id -u)" 2>/dev/null | cut -d: -f6)" "${XDG_DATA_HOME:-}"; do
    [ -n "$candidate" ] || continue
    if [ -d "$candidate" ] && [ -w "$candidate" ]; then BASE=$candidate; break; fi
  done
  if [ -z "$BASE" ]; then
    BASE=/tmp/kotatsu-agent-$(id -u)
    mkdir -p -m 0700 "$BASE" || die "could not create $BASE"
  fi
  echo "Kotatsu: installing as user $INSTALL_USER in $BASE"
  INSTALL_DIR=$BASE/.local/bin
  STATE_DIR=$BASE/.local/state/kotatsu-agent
  SERVICE_FILE=$BASE/.config/systemd/user/kotatsu-agent.service
  PROTECT_HOME=false
  WANTED_BY=default.target
  install -d -m 0700 "$STATE_DIR" || die "could not create $STATE_DIR"
  install -d -m 0755 "$INSTALL_DIR" || die "could not create $INSTALL_DIR"
fi
# Atomic replacement: writing over a running executable fails with ETXTBSY.
install -m 0755 "$WORK/kotatsu-agent" "$INSTALL_DIR/.kotatsu-agent.new" || die "could not stage agent in $INSTALL_DIR"
mv -f "$INSTALL_DIR/.kotatsu-agent.new" "$INSTALL_DIR/kotatsu-agent" || die "could not install agent to $INSTALL_DIR"
STATE="$STATE_DIR/agent.json"
# Opt-in re-enrollment for a revoked machine: KOTATSU_REENROLL=1
if [ "${KOTATSU_REENROLL:-0}" = 1 ]; then
  rm -f "$STATE"
fi
if [ -e "$STATE" ]; then
  echo "Kotatsu: already enrolled; reusing existing machine identity"
else
  echo "Kotatsu: enrolling this machine"
  # Token travels on standard input, never in process arguments or unit files.
  if [ "$AS_ROOT" = 1 ]; then
    printf '%%s\n' "$TOKEN" | runuser -u kotatsu -- "$INSTALL_DIR/kotatsu-agent" enroll --server "$SERVER" --state "$STATE" $DEV_FLAG || die "enrollment failed"
  else
    printf '%%s\n' "$TOKEN" | "$INSTALL_DIR/kotatsu-agent" enroll --server "$SERVER" --state "$STATE" $DEV_FLAG || die "enrollment failed"
  fi
fi
unset TOKEN
start_background() {
  LOG="$STATE_DIR/agent.log"
  if command -v setsid >/dev/null 2>&1; then
    setsid "$@" >>"$LOG" 2>&1 &
  else
    nohup "$@" >>"$LOG" 2>&1 &
  fi
  echo $!
}
if [ "$SYSTEMD_AVAILABLE" = 1 ]; then
  mkdir -p "$(dirname "$SERVICE_FILE")" || die "could not create systemd unit directory"
  cat > "$SERVICE_FILE" <<UNIT
[Unit]
Description=Kotatsu outbound machine agent
After=network-online.target
Wants=network-online.target
[Service]
User=$INSTALL_USER
Group=$INSTALL_USER
ExecStart=$INSTALL_DIR/kotatsu-agent run --state $STATE
Restart=always
RestartSec=5
WorkingDirectory=$STATE_DIR
Environment=HOME=$STATE_DIR
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=$PROTECT_HOME
PrivateTmp=true
ReadWritePaths=$STATE_DIR
UMask=0077
KillMode=control-group
TimeoutStopSec=10
[Install]
WantedBy=$WANTED_BY
UNIT
  if [ "$AS_ROOT" = 1 ]; then
    systemctl daemon-reload
    systemctl enable kotatsu-agent.service || die "could not enable service"
    systemctl restart kotatsu-agent.service || die "could not start service; check journalctl -u kotatsu-agent"
  else
    systemctl --user daemon-reload
    systemctl --user enable kotatsu-agent.service || die "could not enable user service"
    systemctl --user restart kotatsu-agent.service || die "could not start user service; check journalctl --user -u kotatsu-agent"
  fi
else
  PIDFILE="$STATE_DIR/agent.pid"
  if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    echo "Kotatsu: agent already running (pid $(cat "$PIDFILE"))"
  else
    echo "Kotatsu: systemd not available; running agent in background"
    if [ "$AS_ROOT" = 1 ]; then
      AGENT_PID=$(start_background runuser -u kotatsu -- "$INSTALL_DIR/kotatsu-agent" run --state "$STATE")
    else
      AGENT_PID=$(start_background "$INSTALL_DIR/kotatsu-agent" run --state "$STATE")
    fi
    echo "$AGENT_PID" > "$PIDFILE"
    sleep 2
    if [ -n "$AGENT_PID" ] && ! kill -0 "$AGENT_PID" 2>/dev/null; then
      echo "Kotatsu: agent log ($STATE_DIR/agent.log):" >&2
      tail -n 20 "$STATE_DIR/agent.log" >&2 2>/dev/null || true
      die "agent exited immediately"
    fi
    echo "Kotatsu: agent running in background (pid $AGENT_PID), log: $STATE_DIR/agent.log"
  fi
fi
if [ "$AS_ROOT" = 1 ]; then
  echo "Kotatsu: enrolled and started. Shells run as the unprivileged kotatsu user."
else
  echo "Kotatsu: enrolled and started. Shells run as the unprivileged $INSTALL_USER user."
fi
`

func (s *Server) revokeMachine(w http.ResponseWriter, r *http.Request) {
	owner := identity(r).Account.ID
	m, err := s.Store.OwnedMachine(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		fail(w, 404, "machine not found")
		return
	}
	s.mu.Lock()
	if err = s.Store.RevokeMachine(r.Context(), owner, m.DisplayID); err != nil {
		s.mu.Unlock()
		result(w, nil, err)
		return
	}
	link := s.agents[m.ID]
	for _, session := range s.sessions {
		if session.machine == m.ID {
			session.cancel()
		}
	}
	s.mu.Unlock()
	if link != nil {
		_ = link.conn.CloseNow()
	}
	respond(w, 200, map[string]bool{"revoked": true})
}
