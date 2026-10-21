#!/bin/sh
set -eu
[ "$(id -u)" = 0 ] || { echo "Run as root" >&2; exit 1; }
systemctl disable --now anyshell-agent.service
rm -f /etc/systemd/system/anyshell-agent.service /usr/local/bin/anyshell-agent
systemctl daemon-reload
if [ "${1:-}" = --purge ]; then
  rm -rf /var/lib/anyshell
  if getent passwd anyshell >/dev/null; then userdel anyshell; fi
else
  echo "Identity retained at /var/lib/anyshell; pass --purge to remove the credentials and service user."
fi
echo "Service removed. Revoke the machine credential on your server if this host is retired."
