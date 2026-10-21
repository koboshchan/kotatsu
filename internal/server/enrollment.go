package server

import (
	"fmt"
	"github.com/koboshchan/anyshell/internal/api"
	"github.com/koboshchan/anyshell/internal/auth"
	"github.com/koboshchan/anyshell/internal/config"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$`)

func (s *Server) enrollmentRoutes() {
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
	e, err := s.Store.CreateEnrollment(r.Context(), identity(r).Account.ID, in.Reusable, time.Duration(in.TTL)*time.Second)
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
	if auth.Bearer("Bearer "+in.Token, "ae") == "" {
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
	if auth.Bearer("Bearer "+token, "ae") == "" || s.Store.EnrollmentValid(r.Context(), token) != nil {
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
	w.Header().Set("Content-Disposition", "attachment; filename=anyshell-install.sh")
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
die() { echo "AnyShell: $*" >&2; exit 1; }
echo "AnyShell: checking Linux/systemd prerequisites"
[ "$(uname -s)" = Linux ] || die "only Linux is supported"
case "$(uname -m)" in x86_64|amd64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) die "unsupported architecture" ;; esac
[ "$(id -u)" = 0 ] || die "run this installer as root"
command -v systemctl >/dev/null || die "systemd is required"
command -v curl >/dev/null || die "install curl first"
command -v sha256sum >/dev/null || die "install sha256sum first"
command -v tar >/dev/null || die "install tar first"
[ ! -e /var/lib/anyshell/agent.json ] || die "already enrolled; uninstall explicitly before enrolling again"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT HUP INT TERM
PROTO='=https'
[ "$DEVELOPMENT" != true ] || PROTO='=http,https'
fetch() { curl --fail --silent --show-error --proto "$PROTO" --proto-redir "$PROTO" --tlsv1.2 "$1" -o "$2"; }
ARTIFACT="anyshell-agent_${VERSION}_linux_${ARCH}.tar.gz"
echo "AnyShell: downloading pinned release $VERSION for $ARCH"
fetch "$RELEASE_ORIGIN/releases/$VERSION/$ARTIFACT" "$WORK/$ARTIFACT" || die "agent download failed"
fetch "$RELEASE_ORIGIN/releases/$VERSION/SHA256SUMS" "$WORK/SHA256SUMS" || die "checksum download failed"
(cd "$WORK"; grep "  $ARTIFACT$" SHA256SUMS > selected; [ "$(wc -l < selected)" -eq 1 ] && sha256sum -c selected) || die "checksum verification failed; nothing installed"
tar -tzf "$WORK/$ARTIFACT" > "$WORK/contents"
[ "$(cat "$WORK/contents")" = anyshell-agent ] || die "unexpected release archive contents"
tar -xzf "$WORK/$ARTIFACT" -C "$WORK" --no-same-owner
[ -f "$WORK/anyshell-agent" ] && [ ! -L "$WORK/anyshell-agent" ] || die "invalid agent artifact"
echo "AnyShell: creating restricted service account"
getent passwd anyshell >/dev/null || useradd --system --home-dir /var/lib/anyshell --create-home --shell /usr/sbin/nologin anyshell
install -d -m 0700 -o anyshell -g anyshell /var/lib/anyshell
install -m 0755 "$WORK/anyshell-agent" /usr/local/bin/anyshell-agent
echo "AnyShell: enrolling this machine"
# Token travels on standard input, never in process arguments or unit files.
printf '%%s\n' "$TOKEN" | runuser -u anyshell -- /usr/local/bin/anyshell-agent enroll --server "$SERVER" --state /var/lib/anyshell/agent.json $DEV_FLAG || die "enrollment failed"
unset TOKEN
cat > /etc/systemd/system/anyshell-agent.service <<'UNIT'
[Unit]
Description=AnyShell outbound machine agent
After=network-online.target
Wants=network-online.target
[Service]
User=anyshell
Group=anyshell
ExecStart=/usr/local/bin/anyshell-agent run --state /var/lib/anyshell/agent.json
Restart=always
RestartSec=5
WorkingDirectory=/var/lib/anyshell
Environment=HOME=/var/lib/anyshell
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/anyshell
UMask=0077
KillMode=control-group
TimeoutStopSec=10
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now anyshell-agent.service || die "could not start service; check journalctl -u anyshell-agent"
echo "AnyShell: enrolled and started. Shells run as the unprivileged anyshell user."
`
